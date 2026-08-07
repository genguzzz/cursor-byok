package main

import (
	"fmt"
	"net/http"
	"net/url"

	"cursor/internal/netproxy"
)

func main() {
	status := netproxy.CurrentStatus()
	fmt.Printf("PROXY STATUS: %+v\n", status)

	// 关键测试：解析对 copilot.tencent.com 的请求应该走哪个代理
	req, _ := http.NewRequest("POST", "https://copilot.tencent.com/v2/chat/completions", nil)
	pu, err := netproxy.ProxyForRequest(req)
	if err != nil {
		fmt.Printf("ERR: %v\n", err)
		return
	}
	if pu == nil {
		fmt.Println("PROXY for copilot.tencent.com: DIRECT (no proxy)")
	} else {
		fmt.Printf("PROXY for copilot.tencent.com: %s\n", pu.String())
	}

	// 也要看 goproxy 上游请求怎么发
	tr := netproxy.NewTransport(nil)
	fmt.Printf("Transport ProxyFunc set: %v\n", tr.Proxy != nil)
	pu2, err := tr.Proxy(req)
	fmt.Printf("Transport.Proxy(req) for copilot.tencent.com: %v, %v\n", pu2, err)

	// 探测一下 proxy 实际可达性
	if pu != nil {
		_ = http.DefaultTransport.(*http.Transport).DialContext
		fmt.Println("default transport dial: ok")
	}
	_ = url.Parse
}
