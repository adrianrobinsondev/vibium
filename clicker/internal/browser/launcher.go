package browser

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/vibium/clicker/internal/bidi"
	"github.com/vibium/clicker/internal/log"
	"github.com/vibium/clicker/internal/paths"
	"github.com/vibium/clicker/internal/process"
)

// prefixWriter wraps an io.Writer and prepends a prefix to each line.
type prefixWriter struct {
	w      io.Writer
	prefix string
	atBOL  bool // at beginning of line
}

func newPrefixWriter(w io.Writer, prefix string) *prefixWriter {
	return &prefixWriter{w: w, prefix: prefix, atBOL: true}
}

func (pw *prefixWriter) Write(p []byte) (n int, err error) {
	for _, b := range p {
		if pw.atBOL {
			if _, err := pw.w.Write([]byte(pw.prefix)); err != nil {
				return n, err
			}
			pw.atBOL = false
		}
		if _, err := pw.w.Write([]byte{b}); err != nil {
			return n, err
		}
		n++
		if b == '\n' {
			pw.atBOL = true
		}
	}
	return n, nil
}

// LaunchOptions contains options for launching the browser.
type LaunchOptions struct {
	Headless bool
	Port     int  // Chromedriver port, 0 = auto-select
	Verbose  bool // Show chromedriver output
}

// LaunchResult contains the result of launching the browser via chromedriver.
type LaunchResult struct {
	BidiConn        *bidi.Connection // non-nil when session created via BiDi (no HTTP)
	WebSocketURL    string           // set when session created via HTTP fallback
	SessionID       string
	ChromedriverCmd *exec.Cmd
	Port            int
	UserDataDir     string // Chrome temp profile dir — cleaned up on Close()
}

// sessionRequest is the payload for creating a new session.
type sessionRequest struct {
	Capabilities capabilities `json:"capabilities"`
}

type capabilities struct {
	AlwaysMatch alwaysMatch `json:"alwaysMatch"`
}

type alwaysMatch struct {
	BrowserName  string   `json:"browserName"`
	WebSocketURL bool     `json:"webSocketUrl"`
	Args         []string `json:"goog:chromeOptions,omitempty"`
}

type chromeOptions struct {
	Args   []string `json:"args,omitempty"`
	Binary string   `json:"binary,omitempty"`
}

// sessionResponse is the response from creating a new session.
type sessionResponse struct {
	Value sessionValue `json:"value"`
}

type sessionValue struct {
	SessionID    string                 `json:"sessionId"`
	Capabilities map[string]interface{} `json:"capabilities"`
}

// Launch starts chromedriver and creates a BiDi session.
func Launch(opts LaunchOptions) (*LaunchResult, error) {
	log.Debug("launching browser", "headless", opts.Headless)

	chromedriverPath, err := paths.GetChromedriverPath()
	if err != nil {
		return nil, fmt.Errorf("chromedriver not found")
	}
	log.Debug("found chromedriver", "path", chromedriverPath)

	chromePath, err := paths.GetChromeExecutable()
	if err != nil {
		return nil, fmt.Errorf("Chrome not found")
	}
	log.Debug("found chrome", "path", chromePath)

	// Find available port
	port := opts.Port
	if port == 0 {
		port, err = findAvailablePort()
		if err != nil {
			return nil, fmt.Errorf("failed to find available port: %w", err)
		}
	}
	log.Debug("using port", "port", port)

	// Start chromedriver as a process group leader so we can kill all children
	cmd := exec.Command(chromedriverPath, fmt.Sprintf("--port=%d", port))
	setProcGroup(cmd)
	if opts.Verbose {
		fmt.Println("       ------- chromedriver -------")
		pw := newPrefixWriter(os.Stdout, "       ")
		cmd.Stdout = pw
		cmd.Stderr = pw
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("failed to start chromedriver: %w", err)
	}

	// Track for cleanup
	process.Track(cmd)

	// Wait for chromedriver to be ready
	baseURL := fmt.Sprintf("http://localhost:%d", port)
	if err := waitForChromedriver(baseURL, 10*time.Second); err != nil {
		cmd.Process.Kill()
		return nil, fmt.Errorf("chromedriver failed to start: %w", err)
	}

	if opts.Verbose {
		fmt.Println("       ----------------------------")
	}

	// Try BiDi session.new first (direct WebSocket, no HTTP round-trip)
	wsURL := fmt.Sprintf("ws://localhost:%d/session", port)
	conn, connErr := bidi.Connect(wsURL)
	if connErr == nil {
		client := bidi.NewClient(conn)
		caps := buildCapabilities(chromePath, opts.Headless)
		result, sessionErr := client.SessionNew(caps)
		if sessionErr == nil {
			userDataDir, _ := result.Capabilities["userDataDir"].(string)
			log.Info("browser launched via BiDi session.new", "sessionId", result.SessionID)
			return &LaunchResult{
				BidiConn:        conn,
				SessionID:       result.SessionID,
				ChromedriverCmd: cmd,
				Port:            port,
				UserDataDir:     userDataDir,
			}, nil
		}
		log.Debug("BiDi session.new failed, falling back to HTTP", "error", sessionErr)
		conn.Close()
	} else {
		log.Debug("BiDi WebSocket connect failed, falling back to HTTP", "error", connErr)
	}

	// Fallback: HTTP POST /session (original path)
	sessionID, httpWsURL, userDataDir, err := createSession(baseURL, chromePath, opts.Headless, opts.Verbose)
	if err != nil {
		cmd.Process.Kill()
		return nil, fmt.Errorf("failed to create session: %w", err)
	}
	log.Info("browser launched via HTTP", "sessionId", sessionID, "wsUrl", httpWsURL)

	return &LaunchResult{
		WebSocketURL:    httpWsURL,
		SessionID:       sessionID,
		ChromedriverCmd: cmd,
		Port:            port,
		UserDataDir:     userDataDir,
	}, nil
}

// findAvailablePort finds an available TCP port.
func findAvailablePort() (int, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer listener.Close()
	return listener.Addr().(*net.TCPAddr).Port, nil
}

// waitForChromedriver waits for chromedriver to be ready.
func waitForChromedriver(baseURL string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		resp, err := http.Get(baseURL + "/status")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("timeout waiting for chromedriver")
}

// chromeArgs returns the standard Chrome launch arguments.
func chromeArgs(headless bool) []string {
	args := []string{
		"--no-first-run",
		"--no-default-browser-check",
		"--disable-infobars",
		"--disable-blink-features=AutomationControlled",
		"--disable-crash-reporter",
		"--disable-background-networking",
		"--disable-background-timer-throttling",
		"--disable-backgrounding-occluded-windows",
		"--disable-breakpad",
		"--disable-component-extensions-with-background-pages",
		"--disable-component-update",
		"--disable-default-apps",
		"--disable-dev-shm-usage",
		"--disable-extensions",
		"--disable-notifications",
		"--disable-features=TranslateUI,PasswordLeakDetection",
		"--disable-hang-monitor",
		"--disable-ipc-flooding-protection",
		"--disable-popup-blocking",
		"--disable-prompt-on-repost",
		"--disable-renderer-backgrounding",
		"--disable-sync",
		"--enable-features=NetworkService,NetworkServiceInProcess",
		"--force-color-profile=srgb",
		"--metrics-recording-only",
		"--password-store=basic",
		"--use-mock-keychain",
	}
	args = append(args, platformChromeArgs()...)
	if headless {
		args = append(args, "--headless=new")
	}
	return args
}

// buildCapabilities returns the capabilities map for BiDi session.new.
func buildCapabilities(chromePath string, headless bool) map[string]interface{} {
	return map[string]interface{}{
		"alwaysMatch": map[string]interface{}{
			"browserName":  "chrome",
			"webSocketUrl": true,
			"unhandledPromptBehavior": map[string]interface{}{
				"default": "ignore",
			},
			"goog:chromeOptions": map[string]interface{}{
				"binary":          chromePath,
				"args":            chromeArgs(headless),
				"excludeSwitches": []string{"enable-automation", "enable-logging"},
				"prefs": map[string]interface{}{
					"credentials_enable_service":                          false,
					"profile.password_manager_enabled":                    false,
					"profile.password_manager_leak_detection":             false,
					"profile.default_content_setting_values.notifications": 2,
				},
			},
		},
	}
}

// createSession creates a new WebDriver session with BiDi enabled via HTTP.
func createSession(baseURL, chromePath string, headless, verbose bool) (string, string, string, error) {
	reqBody := map[string]interface{}{
		"capabilities": buildCapabilities(chromePath, headless),
	}

	jsonBody, err := json.Marshal(reqBody)
	if err != nil {
		return "", "", "", err
	}

	if verbose {
		fmt.Println("       ------- POST /session -------")
		fmt.Printf("       --> %s\n", string(jsonBody))
	}

	resp, err := http.Post(baseURL+"/session", "application/json", bytes.NewReader(jsonBody))
	if err != nil {
		return "", "", "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return "", "", "", fmt.Errorf("failed to create session: HTTP %d", resp.StatusCode)
	}

	// Read response body for logging and parsing
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", "", "", fmt.Errorf("failed to read session response: %w", err)
	}

	if verbose {
		fmt.Printf("       <-- %s\n", string(respBody))
		fmt.Println("       ------------------------------")
	}

	var sessResp sessionResponse
	if err := json.Unmarshal(respBody, &sessResp); err != nil {
		return "", "", "", fmt.Errorf("failed to decode session response: %w", err)
	}

	wsURL, ok := sessResp.Value.Capabilities["webSocketUrl"].(string)
	if !ok || wsURL == "" {
		return "", "", "", fmt.Errorf("webSocketUrl not found in session capabilities")
	}

	// Extract the Chrome user-data-dir so we can clean it up on Close()
	userDataDir, _ := sessResp.Value.Capabilities["userDataDir"].(string)

	return sessResp.Value.SessionID, wsURL, userDataDir, nil
}

// Close terminates a chromedriver session and process.
func (r *LaunchResult) Close() error {
	log.Debug("closing browser", "sessionId", r.SessionID)

	// Kill chromedriver and all its descendants
	if r.ChromedriverCmd != nil && r.ChromedriverCmd.Process != nil {
		pid := r.ChromedriverCmd.Process.Pid

		// Kill the entire process tree (chromedriver + Chrome + all helpers)
		killProcessTree(pid)

		// Wait for chromedriver to exit
		r.ChromedriverCmd.Wait()

		process.Untrack(r.ChromedriverCmd)
	}

	// Clean up the Chrome temp profile directory
	if r.UserDataDir != "" {
		log.Debug("removing Chrome user data dir", "path", r.UserDataDir)
		os.RemoveAll(r.UserDataDir)
	}

	// Clean up orphaned Chrome temp directories
	cleanupChromeTempDirs()

	return nil
}

// killProcessTree kills a process and all its descendants using process group kill.
// Chromedriver is started as a process group leader (Setpgid: true), so killing
// the group atomically terminates all children — no racy pgrep walk needed.
func killProcessTree(pid int) {
	killProcessGroup(pid)
	killByPid(pid) // fallback: kill root directly if pgid lookup failed
	waitForProcessDead(pid, 2*time.Second)
}

// KillOrphanedChromeProcesses finds and kills Chrome/chromedriver processes
// that have been orphaned (reparented to init/launchd).
func KillOrphanedChromeProcesses() {
	// Kill orphaned chromedriver and Chrome for Testing processes
	patterns := []string{"chromedriver", "Chrome for Testing"}

	for _, pattern := range patterns {
		cmd := exec.Command("pgrep", "-f", pattern)
		output, err := cmd.Output()
		if err != nil {
			continue
		}

		lines := bytes.Split(bytes.TrimSpace(output), []byte("\n"))
		for _, line := range lines {
			if len(line) == 0 {
				continue
			}
			var pid int
			if _, err := fmt.Sscanf(string(line), "%d", &pid); err == nil {
				// Check if this process's parent is 1 (orphaned)
				ppidCmd := exec.Command("ps", "-o", "ppid=", "-p", fmt.Sprintf("%d", pid))
				ppidOut, err := ppidCmd.Output()
				if err != nil {
					continue
				}
				var ppid int
				if _, err := fmt.Sscanf(string(bytes.TrimSpace(ppidOut)), "%d", &ppid); err == nil {
					if ppid == 1 {
						killProcessGroup(pid)
						killByPid(pid)
					}
				}
			}
		}
	}
}

// cleanupChromeTempDirs removes orphaned temp directories created by Chrome for Testing.
// Chrome creates these in os.TempDir() and doesn't clean them up when force-killed.
func cleanupChromeTempDirs() {
	tmpDir := os.TempDir()
	patterns := []string{
		filepath.Join(tmpDir, "com.google.chrome.for.testing.*"),
		filepath.Join(tmpDir, "org.chromium.Chromium.scoped_dir.*"),
	}
	var count int
	for _, pattern := range patterns {
		matches, _ := filepath.Glob(pattern)
		for _, m := range matches {
			if os.RemoveAll(m) == nil {
				count++
			}
		}
	}
	if count > 0 {
		log.Debug("cleaned up Chrome temp dirs", "count", count)
	}
}

