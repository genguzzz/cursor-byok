package proxydebugger

import "time"

const (
	// 默认代理端口 9092，避开本机 Proxyman 常用的 9090。
	defaultProxyAddr = "127.0.0.1:9092"
	defaultUIAddr    = "127.0.0.1:9091"
	// 默认与桌面 MITM 白名单一致，抓全部 *.cursor.sh（mixed 多 server）。
	defaultTargetHost = "*.cursor.sh"
	// 默认按约 200 MiB 字节预算保留抓包；超出时丢弃最早的记录。
	defaultMaxStoreBytes   = 200 << 20
	// 单侧 raw 抓取上限；官方 RunSSE 常超过 2MiB，过小会导致 frames 残缺。
	defaultMaxCaptureBytes = 16 << 20
	defaultMaxFrames       = 2000
	// MaxExchanges=0 表示不按条数上限截断（只按字节预算）。
	defaultMaxExchanges = 0

	CaptureSourceClient   = "client"
	CaptureSourceUpstream = "upstream"

	// ServerLocal / ServerOfficial 给面板「Server」列用：本地助手 vs 官方 Cursor。
	ServerLocal    = "local"
	ServerOfficial = "official"
)

// Config controls the standalone proxy debugger.
type Config struct {
	ProxyAddr string
	UIAddr    string
	// TargetHost 需要解密的主机模式：单个 host、逗号分隔列表，或 `*.cursor.sh`。
	TargetHost string
	// UpstreamProxy 可选。例如本地模式 MITM `http://127.0.0.1:18080`，
	// 用于 Cursor → 调试代理 → 本地 MITM → backend 的抓包链路。
	UpstreamProxy string
	// MaxStoreBytes 抓包内存预算（含 rawHex / decodedJson / frames）。默认 200 MiB。
	MaxStoreBytes int64
	// MaxExchanges 可选条数上限；0 表示不按条数限制，只按 MaxStoreBytes。
	MaxExchanges    int
	MaxCaptureBytes int
	MaxFrames       int

	targetHostPatterns []string
}

func (config Config) normalized() Config {
	if config.ProxyAddr == "" {
		config.ProxyAddr = defaultProxyAddr
	}
	if config.UIAddr == "" {
		config.UIAddr = defaultUIAddr
	}
	if config.TargetHost == "" {
		config.TargetHost = defaultTargetHost
	}
	config.targetHostPatterns = parseTargetHostPatterns(config.TargetHost)
	if config.MaxStoreBytes <= 0 {
		config.MaxStoreBytes = defaultMaxStoreBytes
	}
	if config.MaxExchanges < 0 {
		config.MaxExchanges = defaultMaxExchanges
	}
	if config.MaxCaptureBytes <= 0 {
		config.MaxCaptureBytes = defaultMaxCaptureBytes
	}
	if config.MaxFrames <= 0 {
		config.MaxFrames = defaultMaxFrames
	}
	return config
}

// ExchangeSummary is the compact request-list representation.
type ExchangeSummary struct {
	ID            string    `json:"id"`
	StartedAt     time.Time `json:"startedAt"`
	Method        string    `json:"method"`
	URL           string    `json:"url"`
	Host          string    `json:"host"`
	Path          string    `json:"path"`
	Status        int       `json:"status"`
	State         string    `json:"state"`
	DurationMS    int64     `json:"durationMs"`
	RequestBytes  int64     `json:"requestBytes"`
	ResponseBytes int64     `json:"responseBytes"`
	RequestID     string    `json:"requestId,omitempty"`
	RequestKind   string    `json:"requestKind,omitempty"`
	ResponseKind  string    `json:"responseKind,omitempty"`
	CaptureSource string    `json:"captureSource,omitempty"`
	// Server 标明流量归属：local=本地助手，official=官方 Cursor。
	Server     string `json:"server,omitempty"`
	FrameCount int    `json:"frameCount"`
	Error      string `json:"error,omitempty"`
	// StoredBytes 是该记录在调试器内存中的近似占用（用于预算淘汰）。
	StoredBytes int64 `json:"storedBytes,omitempty"`
}

// Exchange contains the request and response detail shown by the debugger.
type Exchange struct {
	ExchangeSummary
	Request  Payload `json:"request"`
	Response Payload `json:"response"`
}

// Payload contains headers, captured raw bytes, and decoded protobuf frames.
type Payload struct {
	Headers      []Header    `json:"headers"`
	ContentType  string      `json:"contentType,omitempty"`
	ContentCodec string      `json:"contentCodec,omitempty"`
	Size         int64       `json:"size"`
	RawHex       string      `json:"rawHex,omitempty"`
	RawTruncated bool        `json:"rawTruncated,omitempty"`
	DecodedJSON  string      `json:"decodedJson,omitempty"`
	DecodeError  string      `json:"decodeError,omitempty"`
	Frames       []FrameView `json:"frames,omitempty"`
}

// Header is a stable, sorted HTTP header pair.
type Header struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// FrameView describes one Connect streaming envelope.
type FrameView struct {
	Index       int    `json:"index"`
	Flags       uint8  `json:"flags"`
	Length      int    `json:"length"`
	Compressed  bool   `json:"compressed"`
	EndStream   bool   `json:"endStream"`
	Kind        string `json:"kind,omitempty"`
	MessageType string `json:"messageType,omitempty"`
	RequestID   string `json:"requestId,omitempty"`
	JSON        string `json:"json,omitempty"`
	RawHex      string `json:"rawHex,omitempty"`
	Error       string `json:"error,omitempty"`
}

type storeEvent struct {
	Type string `json:"type"`
	ID   string `json:"id,omitempty"`
}

// StoreStats reports current capture buffer usage.
type StoreStats struct {
	Count         int   `json:"count"`
	UsedBytes     int64 `json:"usedBytes"`
	MaxStoreBytes int64 `json:"maxStoreBytes"`
	MaxExchanges  int   `json:"maxExchanges"`
}

// ExchangeQuery describes filters for GET /api/exchanges/query.
type ExchangeQuery struct {
	Server        string
	CaptureSource string
	RequestKind   string
	ResponseKind  string
	Method        string
	HostContains  string
	PathContains  string
	RequestID     string
	ID            string
	Q             string // 模糊匹配 id/path/kind/requestId/decoded/error
	Status        int
	HasRaw        *bool
	HasDecoded    *bool
	MinReqBytes   int64
	MinRespBytes  int64
	Since         time.Time
	Until         time.Time
	Limit         int
	Offset        int
	// Include controls detail payload: summary (default), decoded, raw, frames, full.
	Include string
}
