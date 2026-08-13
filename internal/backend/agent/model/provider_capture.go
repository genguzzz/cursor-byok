package modeladapter

import (
	"bytes"
	"io"
	"net/http"
	"time"
)

const providerTrafficCaptureLimit = 16 << 20

type providerResponseCapture struct {
	io.ReadCloser
	hop       ProviderTrafficHop
	body      bytes.Buffer
	completed bool
}

func (capture *providerResponseCapture) Read(buffer []byte) (int, error) {
	count, err := capture.ReadCloser.Read(buffer)
	if count > 0 && capture.body.Len() < providerTrafficCaptureLimit {
		remaining := providerTrafficCaptureLimit - capture.body.Len()
		if count > remaining {
			count = remaining
		}
		_, _ = capture.body.Write(buffer[:count])
	}
	if err == io.EOF {
		capture.emit("")
	}
	return count, err
}

func (capture *providerResponseCapture) Close() error {
	capture.emit("")
	return capture.ReadCloser.Close()
}

func (capture *providerResponseCapture) emit(errorText string) {
	if capture == nil || capture.completed {
		return
	}
	capture.completed = true
	capture.hop.ResponseBody = append([]byte(nil), capture.body.Bytes()...)
	capture.hop.Duration = time.Since(capture.hop.StartedAt)
	capture.hop.Error = errorText
	emitProviderTrafficCapture(capture.hop)
}

func cloneHeader(header http.Header) map[string][]string {
	if header == nil {
		return nil
	}
	result := make(map[string][]string, len(header))
	for key, values := range header {
		result[key] = append([]string(nil), values...)
	}
	return result
}
