package agentcli

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"
)

// Run 包装官方 agent，指向本地 backend。返回子进程退出码。
func Run(userArgs []string, stdout io.Writer, stderr io.Writer, stdin io.Reader) int {
	backendURL := LoadBackendBaseURL()
	if err := WaitReady(backendURL, 3*time.Second); err != nil {
		fmt.Fprintf(stderr, "本地模式 backend 未就绪（%s）: %v\n请先打开菜单栏「本地模式」，或运行 cursor-local-assistant。\n", backendURL, err)
		return 1
	}
	bin, err := LookUpAgent()
	if err != nil {
		fmt.Fprintln(stderr, err.Error())
		return 1
	}
	cmd := exec.Command(bin, BuildAgentArgs(backendURL, userArgs)...)
	cmd.Env = FilterProxyEnv(os.Environ(), backendURL)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	cmd.Stdin = stdin
	if err := cmd.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return exitErr.ExitCode()
		}
		fmt.Fprintf(stderr, "启动 agent 失败: %v\n", err)
		return 1
	}
	return 0
}

// WaitReady 检查 backend /healthz。直连 loopback，不走 HTTP(S)_PROXY。
func WaitReady(backendURL string, timeout time.Duration) error {
	baseURL := BackendBaseURL(backendURL)
	client := &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			Proxy: nil,
			DialContext: (&net.Dialer{
				Timeout: timeout,
			}).DialContext,
			ForceAttemptHTTP2: false,
		},
	}
	resp, err := client.Get(strings.TrimRight(baseURL, "/") + "/healthz")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("healthz status %d", resp.StatusCode)
	}
	return nil
}
