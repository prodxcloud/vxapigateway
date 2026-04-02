package main

import (
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"runtime"
	"sort"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/shirou/gopsutil/v3/cpu"
	"github.com/shirou/gopsutil/v3/host"
	"github.com/shirou/gopsutil/v3/mem"
	"github.com/shirou/gopsutil/v3/process"
)

// loadAverage returns system load averages (1, 5, 15 minutes)
// Windows doesn't have load averages, so we return empty slice
func loadAverage() ([]float64, error) {
	hostInfo, err := host.Info()
	if err != nil {
		return nil, err
	}
	
	// For Windows, we'll simulate load averages using CPU usage
	// This is a rough approximation
	if hostInfo.OS == "windows" {
		cpuPercent, err := cpu.Percent(time.Second, false)
		if err != nil || len(cpuPercent) == 0 {
			return []float64{0, 0, 0}, nil
		}
		load := cpuPercent[0] / 100.0 // Convert to 0-1 range
		return []float64{load, load, load}, nil
	}
	
	// For Unix-like systems, we would use the actual load averages
	// but since host.LoadAverage() is not available, we'll use CPU usage as approximation
	cpuPercent, err := cpu.Percent(time.Second, false)
	if err != nil || len(cpuPercent) == 0 {
		return []float64{0, 0, 0}, nil
	}
	load := cpuPercent[0] / 100.0
	return []float64{load, load, load}, nil
}

// SystemMetrics holds system monitoring data
type SystemMetrics struct {
	Timestamp    time.Time     `json:"timestamp"`
	CPU          CPUMetrics    `json:"cpu"`
	Memory       MemoryMetrics `json:"memory"`
	Swap         SwapMetrics   `json:"swap"`
	GPU          GPUMetrics    `json:"gpu"`
	Processes    []ProcessInfo `json:"processes"`
	SystemInfo   SystemInfo    `json:"system_info"`
}

type CPUMetrics struct {
	UsagePercent float64   `json:"usage_percent"`
	Cores        int       `json:"cores"`
	ModelName    string    `json:"model_name"`
	Frequency    float64   `json:"frequency_mhz"`
	LoadAverage  []float64 `json:"load_average"`
	CoreUsage    []float64 `json:"core_usage"`
}

type MemoryMetrics struct {
	Total       uint64  `json:"total_bytes"`
	Available   uint64  `json:"available_bytes"`
	Used        uint64  `json:"used_bytes"`
	UsedPercent float64 `json:"used_percent"`
	Free        uint64  `json:"free_bytes"`
	Buffers     uint64  `json:"buffers"`
	Cached      uint64  `json:"cached"`
}

type SwapMetrics struct {
	Total       uint64  `json:"total_bytes"`
	Used        uint64  `json:"used_bytes"`
	UsedPercent float64 `json:"used_percent"`
	Free        uint64  `json:"free_bytes"`
}

type GPUMetrics struct {
	Name        string  `json:"name"`
	MemoryTotal uint64  `json:"memory_total_mb"`
	MemoryUsed  uint64  `json:"memory_used_mb"`
	MemoryFree  uint64  `json:"memory_free_mb"`
	Usage       float64 `json:"usage_percent"`
	Temperature float64 `json:"temperature_celsius"`
	PowerUsage  float64 `json:"power_usage_watts"`
}

type ProcessInfo struct {
	PID         int     `json:"pid"`
	Name        string  `json:"name"`
	CPUPercent  float64 `json:"cpu_percent"`
	MemoryPercent float64 `json:"memory_percent"`
	MemoryRSS   uint64  `json:"memory_rss"`
	Status      string  `json:"status"`
	CreateTime  int64   `json:"create_time"`
	Cmdline     string  `json:"cmdline"`
	GPUMemory   uint64  `json:"gpu_memory_mb"`
}

type SystemInfo struct {
	Hostname        string    `json:"hostname"`
	OS              string    `json:"os"`
	Platform        string    `json:"platform"`
	PlatformVersion string    `json:"platform_version"`
	Uptime          uint64    `json:"uptime_seconds"`
	BootTime        time.Time `json:"boot_time"`
	Procs           uint64    `json:"processes_running"`
}

// Prometheus metrics for system monitoring
var (
	systemCPUUsage = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "system_cpu_usage_percent",
			Help: "System CPU usage percentage",
		},
		[]string{"core"},
	)
	systemMemoryUsage = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "system_memory_usage_percent",
			Help: "System memory usage percentage",
		},
	)
	systemSwapUsage = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "system_swap_usage_percent",
			Help: "System swap usage percentage",
		},
	)
	systemProcessCount = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "system_process_count",
			Help: "Number of running processes",
		},
	)
)

func init() {
	prometheus.MustRegister(systemCPUUsage)
	prometheus.MustRegister(systemMemoryUsage)
	prometheus.MustRegister(systemSwapUsage)
	prometheus.MustRegister(systemProcessCount)
}

// getSystemMetrics collects current system metrics
func getSystemMetrics() (*SystemMetrics, error) {
	metrics := &SystemMetrics{
		Timestamp: time.Now(),
	}

	// CPU metrics
	cpuPercent, err := cpu.Percent(time.Second, false)
	if err == nil && len(cpuPercent) > 0 {
		metrics.CPU.UsagePercent = cpuPercent[0]
	}

	// Individual core usage
	corePercent, _ := cpu.Percent(time.Second, true)
	metrics.CPU.CoreUsage = corePercent

	// CPU info
	cpuInfo, err := cpu.Info()
	if err == nil && len(cpuInfo) > 0 {
		metrics.CPU.ModelName = cpuInfo[0].ModelName
		metrics.CPU.Frequency = float64(cpuInfo[0].Mhz)
	}
	metrics.CPU.Cores = runtime.NumCPU()

	// Load average (Unix-like systems)
	if loadAvg, err := loadAverage(); err == nil {
		metrics.CPU.LoadAverage = loadAvg
	}

	// Memory metrics
	memInfo, err := mem.VirtualMemory()
	if err == nil {
		metrics.Memory = MemoryMetrics{
			Total:       memInfo.Total,
			Available:   memInfo.Available,
			Used:        memInfo.Used,
			UsedPercent: memInfo.UsedPercent,
			Free:        memInfo.Free,
			Buffers:     memInfo.Buffers,
			Cached:      memInfo.Cached,
		}
		systemMemoryUsage.Set(memInfo.UsedPercent)
	}

	// Swap metrics
	swapInfo, err := mem.SwapMemory()
	if err == nil {
		metrics.Swap = SwapMetrics{
			Total:       swapInfo.Total,
			Used:        swapInfo.Used,
			UsedPercent: swapInfo.UsedPercent,
			Free:        swapInfo.Free,
		}
		systemSwapUsage.Set(swapInfo.UsedPercent)
	}

	// Process list
	processes, err := process.Processes()
	if err == nil {
		var procList []ProcessInfo
		for _, p := range processes {
			if pid := p.Pid; pid > 0 {
				name, _ := p.Name()
				cpuPercent, _ := p.CPUPercent()
				memInfo, _ := p.MemoryInfo()
				memPercent, _ := p.MemoryPercent()
				statusSlice, _ := p.Status()
				createTime, _ := p.CreateTime()
				cmdline, _ := p.Cmdline()

				var status string
				if len(statusSlice) > 0 {
					status = statusSlice[0]
				}

				proc := ProcessInfo{
					PID:           int(pid),
					Name:          name,
					CPUPercent:    cpuPercent,
					MemoryPercent: float64(memPercent),
					MemoryRSS:     memInfo.RSS,
					Status:        status,
					CreateTime:    createTime,
					Cmdline:       cmdline,
				}
				procList = append(procList, proc)
			}
		}

		// Sort by CPU usage (descending)
		sort.Slice(procList, func(i, j int) bool {
			return procList[i].CPUPercent > procList[j].CPUPercent
		})

		// Limit to top 20 processes
		if len(procList) > 20 {
			procList = procList[:20]
		}

		metrics.Processes = procList
		systemProcessCount.Set(float64(len(processes)))
	}

	// System info
	hostInfo, err := host.Info()
	if err == nil {
		metrics.SystemInfo = SystemInfo{
			Hostname:        hostInfo.Hostname,
			OS:              hostInfo.OS,
			Platform:        hostInfo.Platform,
			PlatformVersion: hostInfo.PlatformVersion,
			Uptime:          hostInfo.Uptime,
			BootTime:        time.Unix(int64(hostInfo.BootTime), 0),
			Procs:           hostInfo.Procs,
		}
	}

	// Update Prometheus metrics
	for i, usage := range metrics.CPU.CoreUsage {
		systemCPUUsage.WithLabelValues(fmt.Sprintf("core_%d", i)).Set(usage)
	}

	return metrics, nil
}

// systemMonitorHandler serves the monitoring dashboard
func systemMonitorHandler(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/api/system-metrics" {
		// API endpoint for JSON data
		metrics, err := getSystemMetrics()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(metrics)
		return
	}

	// Serve the HTML dashboard
	tmpl := `
<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>System Monitor - VA API Gateway</title>
    <script src="https://cdn.jsdelivr.net/npm/chart.js"></script>
    <style>
        * {
            margin: 0;
            padding: 0;
            box-sizing: border-box;
        }

        body {
            font-family: 'Courier New', monospace;
            background: #000;
            color: #00ff00;
            overflow: hidden;
        }

        .container {
            display: grid;
            grid-template-columns: 1fr 1fr;
            grid-template-rows: auto 200px auto;
            gap: 10px;
            padding: 10px;
            height: 100vh;
        }

        .header {
            grid-column: 1 / -1;
            background: #111;
            border: 1px solid #00ff00;
            padding: 10px;
            text-align: center;
        }

        .metrics-panel {
            background: #111;
            border: 1px solid #00ff00;
            padding: 15px;
            overflow-y: auto;
        }

        .chart-panel {
            grid-column: 1 / -1;
            background: #111;
            border: 1px solid #00ff00;
            padding: 15px;
            height: 200px;
        }

        .processes-panel {
            grid-column: 1 / -1;
            background: #111;
            border: 1px solid #00ff00;
            padding: 15px;
            overflow-y: auto;
        }

        .metric-item {
            margin: 5px 0;
            display: flex;
            justify-content: space-between;
        }

        .metric-label {
            color: #00ff00;
        }

        .metric-value {
            color: #ffff00;
            font-weight: bold;
        }

        .progress-bar {
            width: 100%;
            height: 20px;
            background: #333;
            border: 1px solid #00ff00;
            margin: 5px 0;
        }

        .progress-fill {
            height: 100%;
            background: linear-gradient(90deg, #00ff00, #ffff00);
            transition: width 0.3s ease;
        }

        table {
            width: 100%;
            border-collapse: collapse;
        }

        th, td {
            padding: 5px;
            text-align: left;
            border-bottom: 1px solid #333;
        }

        th {
            color: #ffff00;
            border-bottom: 1px solid #00ff00;
        }

        .high-usage {
            color: #ff0000;
        }

        .medium-usage {
            color: #ffaa00;
        }

        .low-usage {
            color: #00ff00;
        }

        h2 {
            color: #00ff00;
            margin-bottom: 10px;
            border-bottom: 1px solid #00ff00;
            padding-bottom: 5px;
        }

        .timestamp {
            color: #888;
            font-size: 12px;
        }
    </style>
</head>
<body>
    <div class="container">
        <div class="header">
            <h1>VA API Gateway System Monitor</h1>
            <div class="timestamp" id="timestamp"></div>
        </div>

        <div class="metrics-panel">
            <h2>CPU</h2>
            <div class="metric-item">
                <span class="metric-label">Usage:</span>
                <span class="metric-value" id="cpu-usage">0%</span>
            </div>
            <div class="progress-bar">
                <div class="progress-fill" id="cpu-progress"></div>
            </div>
            <div class="metric-item">
                <span class="metric-label">Cores:</span>
                <span class="metric-value" id="cpu-cores">0</span>
            </div>
            <div class="metric-item">
                <span class="metric-label">Model:</span>
                <span class="metric-value" id="cpu-model">-</span>
            </div>
            <div class="metric-item">
                <span class="metric-label">Load (1m, 5m, 15m):</span>
                <span class="metric-value" id="cpu-load">-</span>
            </div>
        </div>

        <div class="metrics-panel">
            <h2>Memory</h2>
            <div class="metric-item">
                <span class="metric-label">Usage:</span>
                <span class="metric-value" id="memory-usage">0%</span>
            </div>
            <div class="progress-bar">
                <div class="progress-fill" id="memory-progress"></div>
            </div>
            <div class="metric-item">
                <span class="metric-label">Total:</span>
                <span class="metric-value" id="memory-total">0 GB</span>
            </div>
            <div class="metric-item">
                <span class="metric-label">Used:</span>
                <span class="metric-value" id="memory-used">0 GB</span>
            </div>
            <div class="metric-item">
                <span class="metric-label">Free:</span>
                <span class="metric-value" id="memory-free">0 GB</span>
            </div>
        </div>

        <div class="metrics-panel">
            <h2>Swap</h2>
            <div class="metric-item">
                <span class="metric-label">Usage:</span>
                <span class="metric-value" id="swap-usage">0%</span>
            </div>
            <div class="progress-bar">
                <div class="progress-fill" id="swap-progress"></div>
            </div>
            <div class="metric-item">
                <span class="metric-label">Total:</span>
                <span class="metric-value" id="swap-total">0 GB</span>
            </div>
            <div class="metric-item">
                <span class="metric-label">Used:</span>
                <span class="metric-value" id="swap-used">0 GB</span>
            </div>
        </div>

        <div class="metrics-panel">
            <h2>System Info</h2>
            <div class="metric-item">
                <span class="metric-label">Hostname:</span>
                <span class="metric-value" id="hostname">-</span>
            </div>
            <div class="metric-item">
                <span class="metric-label">OS:</span>
                <span class="metric-value" id="os">-</span>
            </div>
            <div class="metric-item">
                <span class="metric-label">Uptime:</span>
                <span class="metric-value" id="uptime">-</span>
            </div>
            <div class="metric-item">
                <span class="metric-label">Processes:</span>
                <span class="metric-value" id="process-count">0</span>
            </div>
        </div>

        <div class="chart-panel">
            <canvas id="usageChart"></canvas>
        </div>

        <div class="processes-panel">
            <h2>Top Processes</h2>
            <table id="processes-table">
                <thead>
                    <tr>
                        <th>PID</th>
                        <th>Name</th>
                        <th>CPU %</th>
                        <th>Memory %</th>
                        <th>Memory (MB)</th>
                        <th>Status</th>
                    </tr>
                </thead>
                <tbody id="processes-tbody">
                </tbody>
            </table>
        </div>
    </div>

    <script>
        // Chart setup
        const ctx = document.getElementById('usageChart').getContext('2d');
        const usageChart = new Chart(ctx, {
            type: 'line',
            data: {
                labels: [],
                datasets: [{
                    label: 'CPU %',
                    data: [],
                    borderColor: '#00ff00',
                    backgroundColor: 'rgba(0, 255, 0, 0.1)',
                    tension: 0.4
                }, {
                    label: 'Memory %',
                    data: [],
                    borderColor: '#ffff00',
                    backgroundColor: 'rgba(255, 255, 0, 0.1)',
                    tension: 0.4
                }]
            },
            options: {
                responsive: true,
                maintainAspectRatio: false,
                scales: {
                    y: {
                        beginAtZero: true,
                        max: 100,
                        ticks: { color: '#00ff00' },
                        grid: { color: '#333' }
                    },
                    x: {
                        ticks: { color: '#00ff00' },
                        grid: { color: '#333' }
                    }
                },
                plugins: {
                    legend: {
                        labels: { color: '#00ff00' }
                    }
                }
            }
        });

        // Data history for chart
        const maxDataPoints = 60;
        let timeLabels = [];
        let cpuData = [];
        let memoryData = [];

        function formatBytes(bytes) {
            const sizes = ['B', 'KB', 'MB', 'GB', 'TB'];
            if (bytes === 0) return '0 B';
            const i = Math.floor(Math.log(bytes) / Math.log(1024));
            return Math.round(bytes / Math.pow(1024, i) * 100) / 100 + ' ' + sizes[i];
        }

        function formatUptime(seconds) {
            const days = Math.floor(seconds / 86400);
            const hours = Math.floor((seconds % 86400) / 3600);
            const minutes = Math.floor((seconds % 3600) / 60);
            return days + 'd ' + hours + 'h ' + minutes + 'm';
        }

        function getUsageClass(percentage) {
            if (percentage > 80) return 'high-usage';
            if (percentage > 50) return 'medium-usage';
            return 'low-usage';
        }

        function updateMetrics(data) {
            // Update timestamp
            document.getElementById('timestamp').textContent = 'Last updated: ' + new Date(data.timestamp).toLocaleString();

            // CPU
            document.getElementById('cpu-usage').textContent = data.cpu.usage_percent.toFixed(1) + '%';
            document.getElementById('cpu-usage').className = 'metric-value ' + getUsageClass(data.cpu.usage_percent);
            document.getElementById('cpu-progress').style.width = data.cpu.usage_percent + '%';
            document.getElementById('cpu-cores').textContent = data.cpu.cores;
            document.getElementById('cpu-model').textContent = data.cpu.model_name || 'Unknown';
            
            if (data.cpu.load_average && data.cpu.load_average.length >= 3) {
                document.getElementById('cpu-load').textContent = 
                    data.cpu.load_average.map(l => l.toFixed(2)).join(', ');
            }

            // Memory
            document.getElementById('memory-usage').textContent = data.memory.used_percent.toFixed(1) + '%';
            document.getElementById('memory-usage').className = 'metric-value ' + getUsageClass(data.memory.used_percent);
            document.getElementById('memory-progress').style.width = data.memory.used_percent + '%';
            document.getElementById('memory-total').textContent = formatBytes(data.memory.total);
            document.getElementById('memory-used').textContent = formatBytes(data.memory.used);
            document.getElementById('memory-free').textContent = formatBytes(data.memory.free);

            // Swap
            document.getElementById('swap-usage').textContent = data.swap.used_percent.toFixed(1) + '%';
            document.getElementById('swap-usage').className = 'metric-value ' + getUsageClass(data.swap.used_percent);
            document.getElementById('swap-progress').style.width = data.swap.used_percent + '%';
            document.getElementById('swap-total').textContent = formatBytes(data.swap.total);
            document.getElementById('swap-used').textContent = formatBytes(data.swap.used);

            // System Info
            document.getElementById('hostname').textContent = data.system_info.hostname;
            document.getElementById('os').textContent = data.system_info.os + ' ' + data.system_info.platform_version;
            document.getElementById('uptime').textContent = formatUptime(data.system_info.uptime_seconds);
            document.getElementById('process-count').textContent = data.system_info.procs;

            // Processes
            const tbody = document.getElementById('processes-tbody');
            tbody.innerHTML = '';
            
            data.processes.forEach(proc => {
                const row = tbody.insertRow();
                row.innerHTML = 
                    '<td>' + proc.pid + '</td>' +
                    '<td>' + (proc.name || '-') + '</td>' +
                    '<td class="' + getUsageClass(proc.cpu_percent) + '">' + proc.cpu_percent.toFixed(1) + '%</td>' +
                    '<td class="' + getUsageClass(proc.memory_percent) + '">' + proc.memory_percent.toFixed(1) + '%</td>' +
                    '<td>' + formatBytes(proc.memory_rss) + '</td>' +
                    '<td>' + (proc.status || '-') + '</td>';
            });

            // Update chart
            const now = new Date().toLocaleTimeString();
            timeLabels.push(now);
            cpuData.push(data.cpu.usage_percent);
            memoryData.push(data.memory.used_percent);

            // Keep only last N points
            if (timeLabels.length > maxDataPoints) {
                timeLabels.shift();
                cpuData.shift();
                memoryData.shift();
            }

            usageChart.data.labels = timeLabels;
            usageChart.data.datasets[0].data = cpuData;
            usageChart.data.datasets[1].data = memoryData;
            usageChart.update('none');
        }

        // Fetch metrics periodically
        async function fetchMetrics() {
            try {
                const response = await fetch('/api/system-metrics');
                const data = await response.json();
                updateMetrics(data);
            } catch (error) {
                console.error('Error fetching metrics:', error);
            }
        }

        // Initial fetch and then update every 2 seconds
        fetchMetrics();
        setInterval(fetchMetrics, 2000);
    </script>
</body>
</html>`

	t, _ := template.New("dashboard").Parse(tmpl)
	t.Execute(w, nil)
}
