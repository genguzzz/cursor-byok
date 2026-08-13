// backlog_store.go asynchronously persists stream backlog events in SQLite.
package forwarder

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"time"

	"google.golang.org/protobuf/proto"

	"cursor/gen/agentv1"
	_ "modernc.org/sqlite"
)

const (
	backlogSQLiteBusyTimeoutMS = 2000
	backlogReadBatchSize       = 1024
	backlogMemoryLimitBytes    = 20 << 20
	backlogFlushInterval       = 150 * time.Millisecond
	backlogFlushBytes          = 4 << 20
	backlogLargePayloadLimit   = 1 << 20
	backlogQueueCapacity       = 1024
)

type backlogRecord struct {
	requestID string
	event     StreamEvent
	payload   []byte
	blobID    string
}

type backlogStore struct {
	db           *sql.DB
	queue        chan backlogRecord
	critical     chan backlogRecord
	flushRequest chan chan error
	stop         chan struct{}
	stopOnce     sync.Once
	wg           sync.WaitGroup
	admissionMu  sync.Mutex
	admission    *sync.Cond
	queuedBytes  int64
	closed       bool
}

func newBacklogStore(path string) *backlogStore {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		db, _ = sql.Open("sqlite", ":memory:")
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	ctx := context.Background()
	_, _ = db.ExecContext(ctx, fmt.Sprintf("PRAGMA busy_timeout = %d", backlogSQLiteBusyTimeoutMS))
	_, _ = db.ExecContext(ctx, "PRAGMA journal_mode = WAL")
	_, _ = db.ExecContext(ctx, "PRAGMA synchronous = NORMAL")
	_, _ = db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS backlog (
		request_id TEXT NOT NULL,
		seq INTEGER NOT NULL,
		payload BLOB NOT NULL,
		end INTEGER NOT NULL DEFAULT 0,
		terminal_code TEXT NOT NULL DEFAULT '',
		terminal_msg TEXT NOT NULL DEFAULT '',
		published_at INTEGER NOT NULL DEFAULT 0,
		blob_id TEXT NOT NULL DEFAULT '',
		PRIMARY KEY (request_id, seq)
	)`)
	_, _ = db.ExecContext(ctx, "ALTER TABLE backlog ADD COLUMN blob_id TEXT NOT NULL DEFAULT ''")
	_, _ = db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS backlog_blob (
		blob_id TEXT PRIMARY KEY,
		payload BLOB NOT NULL
	)`)
	store := &backlogStore{
		db:           db,
		queue:        make(chan backlogRecord, backlogQueueCapacity),
		critical:     make(chan backlogRecord, 32),
		flushRequest: make(chan chan error),
		stop:         make(chan struct{}),
	}
	store.admission = sync.NewCond(&store.admissionMu)
	pruneStaleBacklogOnStartup(db)
	store.wg.Add(1)
	go store.run()
	return store
}

// pruneStaleBacklogOnStartup drops backlog rows left over from a previous
// process and reclaims the freed pages with VACUUM. Backlog is a process-local
// buffer whose lifetime is bounded by its active stream: after a broker
// restart no subscriber can resume a stale request, so the leftover rows are
// unreachable garbage. Runs before the writer goroutine starts so VACUUM never
// contends with an in-flight transaction.
func pruneStaleBacklogOnStartup(db *sql.DB) {
	ctx := context.Background()
	res, err := db.ExecContext(ctx, `DELETE FROM backlog`)
	if err != nil {
		return
	}
	deleted, _ := res.RowsAffected()
	if _, err := db.ExecContext(ctx, `DELETE FROM backlog_blob`); err != nil {
		return
	}
	if deleted > 0 {
		_, _ = db.ExecContext(ctx, `VACUUM`)
	}
}

// enqueue performs serialization and admission only. pending records are owned by run.
func (store *backlogStore) enqueue(requestID string, event StreamEvent) error {
	payload, err := marshalBacklogMessage(event.Message)
	if err != nil {
		return err
	}
	if int64(len(payload)) > backlogMemoryLimitBytes {
		return fmt.Errorf("backlog event exceeds memory limit: %d bytes", len(payload))
	}
	record := backlogRecord{requestID: requestID, event: event, payload: payload}
	if len(payload) > backlogLargePayloadLimit {
		record.blobID = fmt.Sprintf("%s:%d", requestID, event.Seq)
		record.event.Message = nil
	}
	store.admissionMu.Lock()
	if store.closed {
		store.admissionMu.Unlock()
		return fmt.Errorf("backlog store is closed")
	}
	store.admissionMu.Unlock()
	if event.End {
		select {
		case store.critical <- record:
			return nil
		case <-store.stop:
			return fmt.Errorf("backlog store is closed")
		}
	}
	size := int64(len(payload))
	store.admissionMu.Lock()
	for store.queuedBytes+size > backlogMemoryLimitBytes && !store.closed {
		store.admission.Wait()
	}
	if store.closed {
		store.admissionMu.Unlock()
		return fmt.Errorf("backlog store is closed")
	}
	store.queuedBytes += size
	store.admissionMu.Unlock()
	select {
	case store.queue <- record:
		return nil
	case <-store.stop:
		store.admissionMu.Lock()
		store.closed = true
		store.admission.Broadcast()
		store.admissionMu.Unlock()
		return fmt.Errorf("backlog store is closed")
	}
}

func marshalBacklogMessage(message *agentv1.AgentServerMessage) ([]byte, error) {
	if message == nil {
		return []byte{}, nil
	}
	payload, err := proto.Marshal(message)
	if err != nil {
		return nil, fmt.Errorf("marshal backlog event: %w", err)
	}
	return payload, nil
}

func (store *backlogStore) run() {
	defer store.wg.Done()
	ticker := time.NewTicker(backlogFlushInterval)
	defer ticker.Stop()
	pending := make([]backlogRecord, 0, backlogQueueCapacity)
	pendingBytes := int64(0)
	for {
		for {
			select {
			case record := <-store.critical:
				pending = append([]backlogRecord{record}, pending...)
				pendingBytes += int64(len(record.payload))
				pending, pendingBytes = store.flushPending(pending, pendingBytes)
			default:
				goto dispatch
			}
		}
	dispatch:
		select {
		case record := <-store.queue:
			pending = append(pending, record)
			pendingBytes += int64(len(record.payload))
			if pendingBytes >= backlogFlushBytes {
				pending, pendingBytes = store.flushPending(pending, pendingBytes)
			}
		case response := <-store.flushRequest:
			for {
				select {
				case record := <-store.queue:
					pending = append(pending, record)
					pendingBytes += int64(len(record.payload))
				default:
					var err error
					for len(pending) > 0 && err == nil {
						pending, pendingBytes = store.flushPending(pending, pendingBytes)
						if len(pending) > 0 {
							err = fmt.Errorf("backlog flush failed")
						}
					}
					response <- err
					goto next
				}
			}
		case <-ticker.C:
			pending, pendingBytes = store.flushPending(pending, pendingBytes)
		case <-store.stop:
			for {
				select {
				case record := <-store.queue:
					pending = append(pending, record)
					pendingBytes += int64(len(record.payload))
				default:
					for len(pending) > 0 {
						before := len(pending)
						pending, pendingBytes = store.flushPending(pending, pendingBytes)
						if len(pending) == before {
							return
						}
					}
					return
				}
			}
		}
	next:
	}
}

func (store *backlogStore) flushPending(pending []backlogRecord, pendingBytes int64) ([]backlogRecord, int64) {
	if len(pending) == 0 {
		return pending, pendingBytes
	}
	batchBytes := int64(0)
	batchCount := 0
	for batchCount < len(pending) {
		size := int64(len(pending[batchCount].payload))
		if batchCount > 0 && batchBytes+size > backlogFlushBytes {
			break
		}
		batchBytes += size
		batchCount++
	}
	batch := pending[:batchCount]
	if err := store.writeBatch(batch); err != nil {
		return pending, pendingBytes
	}
	store.releaseAdmission(batchBytes)
	return pending[batchCount:], pendingBytes - batchBytes
}

func (store *backlogStore) writeBatch(batch []backlogRecord) error {
	tx, err := store.db.BeginTx(context.Background(), nil)
	if err != nil {
		return err
	}
	blobStmt, err := tx.Prepare(`INSERT OR REPLACE INTO backlog_blob(blob_id, payload) VALUES (?, ?)`)
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	backlogStmt, err := tx.Prepare(`INSERT OR REPLACE INTO backlog(request_id, seq, payload, blob_id, end, terminal_code, terminal_msg, published_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		_ = blobStmt.Close()
		_ = tx.Rollback()
		return err
	}
	for _, record := range batch {
		if record.blobID != "" {
			if _, err := blobStmt.Exec(record.blobID, record.payload); err != nil {
				_ = blobStmt.Close()
				_ = backlogStmt.Close()
				_ = tx.Rollback()
				return err
			}
		}
		endFlag := 0
		if record.event.End {
			endFlag = 1
		}
		payload := record.payload
		if record.blobID != "" {
			payload = []byte{}
		}
		if _, err := backlogStmt.Exec(record.requestID, record.event.Seq, payload, record.blobID, endFlag, record.event.TerminalErrorCode, record.event.TerminalErrorMessage, record.event.PublishedAt.UnixNano()); err != nil {
			_ = blobStmt.Close()
			_ = backlogStmt.Close()
			_ = tx.Rollback()
			return err
		}
	}
	_ = blobStmt.Close()
	_ = backlogStmt.Close()
	return tx.Commit()
}

func (store *backlogStore) releaseAdmission(size int64) {
	store.admissionMu.Lock()
	store.queuedBytes -= size
	store.admission.Broadcast()
	store.admissionMu.Unlock()
}

func (store *backlogStore) close() error {
	if store == nil || store.db == nil {
		return nil
	}
	store.stopOnce.Do(func() {
		store.admissionMu.Lock()
		store.closed = true
		store.admission.Broadcast()
		store.admissionMu.Unlock()
		close(store.stop)
	})
	store.wg.Wait()
	return store.db.Close()
}

func (store *backlogStore) flushBarrier() error {
	response := make(chan error, 1)
	select {
	case store.flushRequest <- response:
		return <-response
	case <-store.stop:
		return fmt.Errorf("backlog store is closed")
	}
}

func (store *backlogStore) readAfter(requestID string, cursorSeq uint64) ([]StreamEvent, error) {
	if err := store.flushBarrier(); err != nil {
		return nil, err
	}
	rows, err := store.db.Query(`SELECT b.seq, CASE WHEN b.blob_id != '' THEN bb.payload ELSE b.payload END, b.end, b.terminal_code, b.terminal_msg, b.published_at FROM backlog b LEFT JOIN backlog_blob bb ON b.blob_id = bb.blob_id WHERE b.request_id = ? AND b.seq > ? ORDER BY b.seq ASC LIMIT ?`, requestID, cursorSeq, backlogReadBatchSize)
	if err != nil {
		return nil, fmt.Errorf("query backlog events: %w", err)
	}
	defer rows.Close()
	events := make([]StreamEvent, 0, 64)
	for rows.Next() {
		var event StreamEvent
		var payload []byte
		var endFlag int
		var publishedNano int64
		if err := rows.Scan(&event.Seq, &payload, &endFlag, &event.TerminalErrorCode, &event.TerminalErrorMessage, &publishedNano); err != nil {
			return nil, err
		}
		event.End = endFlag != 0
		event.PublishedAt = time.Unix(0, publishedNano)
		if len(payload) > 0 {
			message := &agentv1.AgentServerMessage{}
			if err := proto.Unmarshal(payload, message); err != nil {
				return nil, err
			}
			event.Message = message
		}
		events = append(events, event)
	}
	return events, rows.Err()
}

func (store *backlogStore) remove(requestID string) error {
	if err := store.flushBarrier(); err != nil {
		return err
	}
	tx, err := store.db.Begin()
	if err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM backlog_blob WHERE blob_id IN (SELECT blob_id FROM backlog WHERE request_id = ? AND blob_id != '')`, requestID); err != nil {
		_ = tx.Rollback()
		return err
	}
	if _, err := tx.Exec(`DELETE FROM backlog WHERE request_id = ?`, requestID); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

func (store *backlogStore) count(requestID string) int {
	_ = store.flushBarrier()
	var count int
	_ = store.db.QueryRow(`SELECT COUNT(*) FROM backlog WHERE request_id = ?`, requestID).Scan(&count)
	return count
}
