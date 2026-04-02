package main

import (
	"fmt"
	"math"
	"os"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/shirou/gopsutil/v3/cpu"
	"github.com/shirou/gopsutil/v3/mem"
)

// ─── ANSI palette ────────────────────────────────────────────────────────────

const (
	clReset   = "\033[0m"
	clBold    = "\033[1m"
	clDim     = "\033[2m"
	clCyan    = "\033[36m"
	clGreen   = "\033[32m"
	clYellow  = "\033[33m"
	clRed     = "\033[31m"
	clMagenta = "\033[35m"
	clBlue    = "\033[34m"
	clWhite   = "\033[97m"
	clGray    = "\033[90m"
	clClear   = "\033[2J\033[H"
	clHide    = "\033[?25l"
	clShow    = "\033[?25h"
)

// ─── Dashboard state ─────────────────────────────────────────────────────────

// DashboardStats is updated by the gateway on every request.
type DashboardStats struct {
	TotalRequests  int64
	ActiveConns    int64
	BlockedIPs     int64
	CacheHits      int64
	CircuitTrips   int64
	RequestHistory [48]int64 // ring buffer for sparkline
	histIdx        int
	mu             sync.Mutex
}

var dashStats = &DashboardStats{}

// RecordRequest increments counters and pushes to the sparkline ring buffer.
func (d *DashboardStats) RecordRequest() {
	atomic.AddInt64(&d.TotalRequests, 1)
	d.mu.Lock()
	d.RequestHistory[d.histIdx%48]++
	d.histIdx++
	d.mu.Unlock()
}

// ─── Bar renderer ────────────────────────────────────────────────────────────

func colorBar(pct float64, width int) string {
	color := clGreen
	switch {
	case pct > 80:
		color = clRed
	case pct > 60:
		color = clYellow
	}
	filled := int(math.Round(pct / 100.0 * float64(width)))
	if filled < 0 {
		filled = 0
	}
	if filled > width {
		filled = width
	}
	return color + strings.Repeat("█", filled) +
		clGray + strings.Repeat("·", width-filled) + clReset
}

func pctLabel(p float64) string { return fmt.Sprintf("%5.1f%%", p) }
func gbLabel(b uint64) string   { return fmt.Sprintf("%.1fG", float64(b)/1e9) }

func uptimeStr(d time.Duration) string {
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	s := int(d.Seconds()) % 60
	if h >= 24 {
		return fmt.Sprintf("%dd %02dh %02dm", h/24, h%24, m)
	}
	return fmt.Sprintf("%02dh %02dm %02ds", h, m, s)
}

// ─── Sparkline ───────────────────────────────────────────────────────────────

var sparkChars = []string{" ", "▁", "▂", "▃", "▄", "▅", "▆", "▇", "█"}

func sparkline(vals [48]int64, width int) string {
	// find max
	var mx int64 = 1
	for _, v := range vals {
		if v > mx {
			mx = v
		}
	}
	var sb strings.Builder
	for i := 0; i < width; i++ {
		v := vals[i%48]
		idx := int(float64(v) / float64(mx) * 8)
		if idx > 8 {
			idx = 8
		}
		sb.WriteString(clCyan + sparkChars[idx] + clReset)
	}
	return sb.String()
}

// ─── Banner (one-shot, always printed) ───────────────────────────────────────

// PrintStartupBanner writes the styled startup banner to stderr.
func PrintStartupBanner(cfg Config, version string) {
	W := 72
	sep := clCyan + "  ╠" + strings.Repeat("═", W-4) + "╣" + clReset
	top := clCyan + "  ╔" + strings.Repeat("═", W-4) + "╗" + clReset
	bot := clCyan + "  ╚" + strings.Repeat("═", W-4) + "╝" + clReset

	row := func(left, right string) string {
		inner := fmt.Sprintf("%-22s%s", left, right)
		pad := W - 4 - len(stripANSI(inner))
		if pad < 0 {
			pad = 0
		}
		return clCyan + "  ║ " + clReset + inner + strings.Repeat(" ", pad) + clCyan + " ║" + clReset
	}
	blank := clCyan + "  ║" + strings.Repeat(" ", W-4) + "║" + clReset

	title := clBold + clWhite + "  VA API Gateway" + clReset +
		clGray + "  ·  v" + version + clReset
	sub := clGray + "  High-performance reverse proxy & AI orchestration layer" + clReset

	fmt.Fprintln(os.Stderr, "\n"+top)
	fmt.Fprintln(os.Stderr, clCyan+"  ║"+clReset+
		centerPad(title, W-4)+clCyan+"║"+clReset)
	fmt.Fprintln(os.Stderr, clCyan+"  ║"+clReset+
		centerPad(sub, W-4)+clCyan+"║"+clReset)
	fmt.Fprintln(os.Stderr, sep)

	fmt.Fprintln(os.Stderr, row(clCyan+"Listen"+clReset, clGreen+"http://0.0.0.0"+cfg.Port+clReset))
	fmt.Fprintln(os.Stderr, row(clCyan+"Load Balancing"+clReset, clWhite+cfg.LoadBalancingAlgo+clReset))
	fmt.Fprintln(os.Stderr, row(clCyan+"Rate Limit"+clReset,
		fmt.Sprintf(clWhite+"%d rps  burst %d"+clReset, cfg.MaxRequestsPerSecond, cfg.MaxBurstSize)))
	fmt.Fprintln(os.Stderr, row(clCyan+"DDoS Threshold"+clReset,
		fmt.Sprintf(clWhite+"%d req/min  block %s"+clReset, cfg.DDoSThreshold, cfg.BlockDuration)))
	fmt.Fprintln(os.Stderr, row(clCyan+"Circuit Breaker"+clReset,
		fmt.Sprintf(clWhite+"max %d failures  reset %s"+clReset, cfg.CircuitBreakerMax, cfg.CircuitBreakerTimeout)))
	fmt.Fprintln(os.Stderr, row(clCyan+"Caching"+clReset,
		boolLabel(cfg.EnableCaching)+" "+clGray+"Redis "+cfg.RedisAddr+clReset))
	fmt.Fprintln(os.Stderr, row(clCyan+"Compression"+clReset, boolLabel(cfg.EnableCompression)))
	fmt.Fprintln(os.Stderr, row(clCyan+"Metrics"+clReset, clGreen+"http://0.0.0.0"+cfg.Port+"/metrics"+clReset))
	fmt.Fprintln(os.Stderr, row(clCyan+"Health"+clReset, clGreen+"http://0.0.0.0"+cfg.Port+"/health"+clReset))
	fmt.Fprintln(os.Stderr, row(clCyan+"Monitor"+clReset, clGreen+"http://0.0.0.0"+cfg.Port+"/monitor"+clReset))

	fmt.Fprintln(os.Stderr, sep)

	// Routes table
	fmt.Fprintln(os.Stderr, clCyan+"  ║ "+clReset+
		clBold+clYellow+fmt.Sprintf("%-18s %-36s %-6s", "PREFIX", "TARGET", "AUTH")+clReset+
		strings.Repeat(" ", 4)+clCyan+"║"+clReset)
	for _, r := range cfg.ServiceRoutes {
		auth := clGray + "no " + clReset
		if r.RequireAuth {
			auth = clGreen + "yes" + clReset
		}
		target := r.TargetURL
		if len(target) > 34 {
			target = target[:31] + "..."
		}
		line := fmt.Sprintf("%-18s %-36s %s", r.Prefix, target, auth)
		pad := W - 4 - len(stripANSI(line)) - 2
		if pad < 0 {
			pad = 0
		}
		fmt.Fprintln(os.Stderr, clCyan+"  ║ "+clReset+line+strings.Repeat(" ", pad)+clCyan+"║"+clReset)
	}

	fmt.Fprintln(os.Stderr, blank)
	fmt.Fprintln(os.Stderr, clCyan+"  ║ "+clReset+
		clGray+"q:quit  s:sort  +/-:speed  Ctrl+C to stop"+clReset+
		strings.Repeat(" ", W-4-44)+clCyan+"║"+clReset)
	fmt.Fprintln(os.Stderr, bot+"\n")
}

func boolLabel(b bool) string {
	if b {
		return clGreen + "enabled " + clReset
	}
	return clGray + "disabled" + clReset
}

func centerPad(s string, width int) string {
	vis := len(stripANSI(s))
	pad := (width - vis) / 2
	if pad < 0 {
		pad = 0
	}
	right := width - vis - pad
	if right < 0 {
		right = 0
	}
	return strings.Repeat(" ", pad) + s + strings.Repeat(" ", right)
}

// stripANSI removes ANSI escape sequences for length calculation.
func stripANSI(s string) string {
	var out strings.Builder
	inEsc := false
	for _, c := range s {
		if c == '\033' {
			inEsc = true
			continue
		}
		if inEsc {
			if c == 'm' {
				inEsc = false
			}
			continue
		}
		out.WriteRune(c)
	}
	return out.String()
}

// ─── Live dashboard ──────────────────────────────────────────────────────────

// LiveDashboard renders an nvtop-style live TUI to stdout.
type LiveDashboard struct {
	gw        *Gateway
	cfg       Config
	version   string
	startTime time.Time
	running   atomic.Bool
	wg        sync.WaitGroup
}

// NewLiveDashboard creates a dashboard bound to the given gateway.
func NewLiveDashboard(gw *Gateway, cfg Config, version string) *LiveDashboard {
	return &LiveDashboard{
		gw:        gw,
		cfg:       cfg,
		version:   version,
		startTime: time.Now(),
	}
}

// Start begins the live refresh loop (non-blocking).
func (d *LiveDashboard) Start(interval time.Duration) {
	if !isTerminal() {
		return
	}
	d.running.Store(true)
	fmt.Print(clHide)
	d.wg.Add(1)
	go func() {
		defer d.wg.Done()
		// warm up CPU sampler
		cpu.Percent(200*time.Millisecond, false)
		cpu.Percent(200*time.Millisecond, true)
		for d.running.Load() {
			d.render()
			time.Sleep(interval)
		}
	}()
}

// Stop halts the refresh loop and restores the cursor.
func (d *LiveDashboard) Stop() {
	d.running.Store(false)
	d.wg.Wait()
	fmt.Print(clShow)
}

func (d *LiveDashboard) render() {
	// ── collect system stats ─────────────────────────────────────────────────
	cpuTotal, _ := cpu.Percent(0, false)
	cpuCores, _ := cpu.Percent(0, true)
	vmem, _ := mem.VirtualMemory()
	smem, _ := mem.SwapMemory()

	uptime := time.Since(d.startTime)
	totalReq := atomic.LoadInt64(&dashStats.TotalRequests)
	activeConn := atomic.LoadInt64(&dashStats.ActiveConns)

	// ── load averages (approximated from CPU) ────────────────────────────────
	var load1 float64
	if len(cpuTotal) > 0 {
		load1 = cpuTotal[0] / 100.0 * float64(runtime.NumCPU())
	}

	// ── build output ─────────────────────────────────────────────────────────
	var sb strings.Builder
	sb.WriteString(clClear)

	W := 78 // total line width

	// ── Header ───────────────────────────────────────────────────────────────
	sb.WriteString(
		clBold + clCyan + "va-gateway" + clReset +
			"  " + clWhite + "VA API Gateway" + clReset +
			"  " + clGray + "v" + d.version + clReset +
			"    " +
			clGray + "up " + clReset + clWhite + uptimeStr(uptime) + clReset +
			"    " +
			clGray + "load " + clReset + clWhite + fmt.Sprintf("%.2f", load1) + clReset +
			"\n\n",
	)

	// ── CPU ──────────────────────────────────────────────────────────────────
	overallCPU := 0.0
	if len(cpuTotal) > 0 {
		overallCPU = cpuTotal[0]
	}
	sb.WriteString(
		clBold + clYellow + "CPU" + clReset +
			fmt.Sprintf("  %d cores", runtime.NumCPU()) +
			"    Overall: " + colorBar(overallCPU, 32) +
			"  " + clWhite + pctLabel(overallCPU) + clReset + "\n",
	)

	// per-core (two columns, up to 16 cores)
	n := len(cpuCores)
	if n > 16 {
		n = 16
	}
	half := (n + 1) / 2
	for i := 0; i < half; i++ {
		// left
		sb.WriteString(fmt.Sprintf("  %s%2d%s  %s  %s%s",
			clGray, i, clReset,
			colorBar(cpuCores[i], 20),
			clWhite, pctLabel(cpuCores[i])+clReset,
		))
		// right
		j := i + half
		if j < n {
			sb.WriteString(fmt.Sprintf("    %s%2d%s  %s  %s%s",
				clGray, j, clReset,
				colorBar(cpuCores[j], 20),
				clWhite, pctLabel(cpuCores[j])+clReset,
			))
		}
		sb.WriteString("\n")
	}
	sb.WriteString("\n")

	// ── MEM ──────────────────────────────────────────────────────────────────
	if vmem != nil {
		sb.WriteString(
			clBold + clBlue + "MEM" + clReset +
				"  " + clGreen + gbLabel(vmem.Used) + " used" + clReset +
				" + " + clGray + gbLabel(vmem.Buffers+vmem.Cached) + " buf/cache" + clReset +
				" / " + clWhite + gbLabel(vmem.Total) + clReset + "\n",
		)
		sb.WriteString("  " + colorBar(vmem.UsedPercent, 50) +
			"  " + clWhite + pctLabel(vmem.UsedPercent) + clReset + "\n")
	}
	if smem != nil && smem.Total > 0 {
		sb.WriteString(
			clBold + clBlue + "SWP" + clReset +
				"  " + gbLabel(smem.Used) + " / " + gbLabel(smem.Total) + "\n",
		)
		sb.WriteString("  " + colorBar(smem.UsedPercent, 50) +
			"  " + clWhite + pctLabel(smem.UsedPercent) + clReset + "\n")
	}
	sb.WriteString("\n")

	// ── Separator ────────────────────────────────────────────────────────────
	sb.WriteString(clGray + strings.Repeat("─", W) + clReset + "\n\n")

	// ── Gateway stats ────────────────────────────────────────────────────────
	cbState := d.gw.circuitBreaker.GetState()
	cbColor := clGreen
	if cbState == "open" {
		cbColor = clRed
	} else if cbState == "half-open" {
		cbColor = clYellow
	}

	sb.WriteString(
		clBold + clMagenta + "GATEWAY" + clReset +
			"  " + clWhite + "VA API Gateway" + clReset +
			"  " + clGray + "port" + clReset + clGreen + d.cfg.Port + clReset +
			"  " + clGray + "algo=" + clReset + clWhite + d.cfg.LoadBalancingAlgo + clReset +
			"  " + clGray + "cb=" + clReset + cbColor + cbState + clReset +
			"\n\n",
	)

	// ── Route / backend table ─────────────────────────────────────────────────
	sb.WriteString(
		clBold + clGray +
			fmt.Sprintf("  %-18s %-32s %-8s %-10s %s\n",
				"PREFIX", "BACKEND", "STATUS", "CONNS", "ALGO") +
			clReset,
	)
	sb.WriteString("  " + clGray + strings.Repeat("·", W-2) + clReset + "\n")

	for _, entry := range d.gw.router.Routes() {
		for _, b := range entry.Pool.Backends() {
			status := clGreen + "● up  " + clReset
			if !b.IsAlive() {
				status = clRed + "○ down" + clReset
			}
			target := b.URL.String()
			if len(target) > 30 {
				target = target[:27] + "..."
			}
			sb.WriteString(fmt.Sprintf("  %s%-18s%s %-32s %s %-10d %s\n",
				clCyan, entry.Prefix, clReset,
				target,
				status,
				b.GetConnections(),
				clGray+d.cfg.LoadBalancingAlgo+clReset,
			))
		}
	}
	sb.WriteString("\n")

	// ── Request counters ─────────────────────────────────────────────────────
	sb.WriteString(
		clBold + clGray +
			fmt.Sprintf("  %-20s %-20s %-20s %s\n",
				"TOTAL REQUESTS", "ACTIVE CONNS", "BLOCKED IPs", "CIRCUIT TRIPS") +
			clReset,
	)
	sb.WriteString(fmt.Sprintf("  %s%-20d%s %-20d %-20d %d\n",
		clWhite, totalReq, clReset,
		activeConn,
		atomic.LoadInt64(&dashStats.BlockedIPs),
		atomic.LoadInt64(&dashStats.CircuitTrips),
	))
	sb.WriteString("\n")

	// ── Request sparkline ─────────────────────────────────────────────────────
	sb.WriteString(clBold + clCyan + "  Request history" + clReset + "\n")
	sb.WriteString(clGray + "  100%\n" + clReset)
	sb.WriteString("  ")
	dashStats.mu.Lock()
	sb.WriteString(sparkline(dashStats.RequestHistory, 48))
	dashStats.mu.Unlock()
	sb.WriteString("\n" + clGray + "    0%\n" + clReset)
	sb.WriteString("\n")

	// ── Footer ───────────────────────────────────────────────────────────────
	sb.WriteString(clGray + strings.Repeat("─", W) + clReset + "\n")
	footer := fmt.Sprintf("  q:quit  s:sort  +/-:speed  1.0s%s v%s",
		strings.Repeat(" ", W-36-len(d.version)), d.version)
	sb.WriteString(clGray + footer + clReset + "\n")

	fmt.Print(sb.String())
}

// ─── Terminal detection ───────────────────────────────────────────────────────

func isTerminal() bool {
	if os.Getenv("NO_DASHBOARD") != "" {
		return false
	}
	fi, err := os.Stdout.Stat()
	if err != nil {
		return false
	}
	return (fi.Mode() & os.ModeCharDevice) != 0
}
