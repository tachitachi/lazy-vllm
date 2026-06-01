//go:build windows

// coretemp-exporter reads Core Temp's shared-memory block and exposes the CPU
// temperature/load/power readings as Prometheus metrics. It runs on the Windows
// host alongside a running Core Temp instance and is scraped like any other
// exporter. Build with: GOOS=windows GOARCH=amd64 go build .
package main

import (
	"bytes"
	"encoding/binary"
	"flag"
	"fmt"
	"log"
	"net/http"
	"unsafe"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"golang.org/x/sys/windows"
)

// CORE_TEMP_SHARED_DATA_EX as documented by the Core Temp shared-memory SDK
// (https://www.alcpu.com/CoreTemp/developers.html). All fields are little-endian
// and the C struct has no internal padding, so it maps 1:1 onto encoding/binary.
type coreTempData struct {
	Load           [256]uint32  // per-core load, 0-100
	TjMax          [128]uint32  // per-CPU junction-max (throttle) temp, Celsius
	CoreCnt        uint32       // physical cores per CPU
	CPUCnt         uint32       // number of CPU packages
	Temp           [256]float32 // per-core temp (see Fahrenheit / DeltaToTjMax)
	VID            float32
	CPUSpeed       float32 // MHz
	FSBSpeed       float32 // MHz
	Multiplier     float32
	CPUName        [100]byte
	Fahrenheit     uint8 // 1 => Temp/TjMax expressed in Fahrenheit
	DeltaToTjMax   uint8 // 1 => Temp holds (TjMax - actual) rather than actual
	TdpSupported   uint8 // struct version 2+
	PowerSupported uint8 // struct version 2+
	StructVersion  uint32
	Tdp            [128]uint32  // watts
	Power          [128]float32 // watts
	Multipliers    [256]float32
}

const (
	fileMapRead = 0x0004
	exSize      = 4740 // sizeof(CORE_TEMP_SHARED_DATA_EX)
	baseSize    = 2686 // sizeof(CORE_TEMP_SHARED_DATA) (v1, no power/tdp)
)

// Core Temp publishes the extended struct under ...Ex; older builds only expose
// the base mapping. Try the richer one first.
var mappingNames = []string{"CoreTempMappingObjectEx", "CoreTempMappingObject"}

// OpenFileMappingW isn't wrapped by x/sys/windows, so bind it directly.
var (
	modkernel32         = windows.NewLazySystemDLL("kernel32.dll")
	procOpenFileMapping = modkernel32.NewProc("OpenFileMappingW")
)

func openFileMapping(access uint32, inherit bool, name *uint16) (windows.Handle, error) {
	var bInherit uintptr
	if inherit {
		bInherit = 1
	}
	r, _, e := procOpenFileMapping.Call(uintptr(access), bInherit, uintptr(unsafe.Pointer(name)))
	if r == 0 {
		if e != windows.ERROR_SUCCESS {
			return 0, e
		}
		return 0, fmt.Errorf("OpenFileMapping(%q) failed", windows.UTF16PtrToString(name))
	}
	return windows.Handle(r), nil
}

func readSharedMemory() (*coreTempData, error) {
	var lastErr error
	for _, name := range mappingNames {
		np, err := windows.UTF16PtrFromString(name)
		if err != nil {
			lastErr = err
			continue
		}
		h, err := openFileMapping(fileMapRead, false, np)
		if err != nil {
			lastErr = fmt.Errorf("open mapping %q: %w", name, err)
			continue
		}

		var addr uintptr
		var mapped int
		for _, sz := range []int{exSize, baseSize} {
			addr, err = windows.MapViewOfFile(h, fileMapRead, 0, 0, uintptr(sz))
			if err == nil {
				mapped = sz
				break
			}
		}
		if err != nil {
			windows.CloseHandle(h)
			lastErr = fmt.Errorf("map view %q: %w", name, err)
			continue
		}

		// Copy out of shared memory immediately, then release the view/handle so
		// we never hold pointers into the mapping. A base-only mapping leaves the
		// trailing (power/tdp) bytes zeroed, which we gate on StructVersion below.
		// addr points into an OS file-mapping view, not Go-managed memory, so the
		// uintptr->Pointer conversion is safe here (GC neither owns nor moves it);
		// we copy out before unmapping. go vet flags this as a heuristic only.
		buf := make([]byte, exSize)
		copy(buf, unsafe.Slice((*byte)(unsafe.Pointer(addr)), mapped))
		windows.UnmapViewOfFile(addr)
		windows.CloseHandle(h)

		var d coreTempData
		if err := binary.Read(bytes.NewReader(buf), binary.LittleEndian, &d); err != nil {
			lastErr = err
			continue
		}
		if d.CoreCnt == 0 || d.CoreCnt > 256 {
			lastErr = fmt.Errorf("implausible core count %d from %q (is Core Temp running?)", d.CoreCnt, name)
			continue
		}
		return &d, nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("no Core Temp shared memory found")
	}
	return nil, lastErr
}

// celsius normalises Temp[idx] to Celsius, applying the delta-to-TjMax and
// Fahrenheit flags. With Core Temp's default Celsius display both flags are 0
// and Temp already holds the actual temperature.
func (d *coreTempData) celsius(idx, cpu uint32) float64 {
	t := float64(d.Temp[idx])
	if d.DeltaToTjMax != 0 {
		t = float64(d.TjMax[cpu]) - t
	}
	if d.Fahrenheit != 0 {
		t = (t - 32) * 5 / 9
	}
	return t
}

func cstr(b []byte) string {
	if i := bytes.IndexByte(b, 0); i >= 0 {
		b = b[:i]
	}
	return string(b)
}

var (
	upDesc      = prometheus.NewDesc("coretemp_up", "1 if Core Temp shared memory was read successfully, 0 otherwise.", nil, nil)
	infoDesc    = prometheus.NewDesc("coretemp_cpu_info", "CPU information from Core Temp; constant 1.", []string{"cpu_name"}, nil)
	tempDesc    = prometheus.NewDesc("coretemp_core_temperature_celsius", "Per-core CPU temperature in Celsius.", []string{"cpu", "core"}, nil)
	loadDesc    = prometheus.NewDesc("coretemp_core_load_ratio", "Per-core CPU load as a ratio 0-1.", []string{"cpu", "core"}, nil)
	tjmaxDesc   = prometheus.NewDesc("coretemp_tjmax_celsius", "Junction max (throttle) temperature in Celsius.", []string{"cpu"}, nil)
	powerDesc   = prometheus.NewDesc("coretemp_power_watts", "CPU package power draw in watts.", []string{"cpu"}, nil)
	tdpDesc     = prometheus.NewDesc("coretemp_tdp_watts", "CPU TDP in watts.", []string{"cpu"}, nil)
	speedDesc   = prometheus.NewDesc("coretemp_cpu_speed_mhz", "Reported CPU core speed in MHz.", nil, nil)
	fsbDesc     = prometheus.NewDesc("coretemp_fsb_speed_mhz", "Reported FSB/base clock in MHz.", nil, nil)
	multDesc    = prometheus.NewDesc("coretemp_multiplier", "Current CPU multiplier.", nil, nil)
	vidDesc     = prometheus.NewDesc("coretemp_vid_volts", "CPU VID in volts.", nil, nil)
	coreCntDesc = prometheus.NewDesc("coretemp_core_count", "Number of physical cores per CPU package.", nil, nil)
	cpuCntDesc  = prometheus.NewDesc("coretemp_cpu_count", "Number of CPU packages.", nil, nil)
)

type collector struct{}

func (collector) Describe(ch chan<- *prometheus.Desc) {
	for _, d := range []*prometheus.Desc{upDesc, infoDesc, tempDesc, loadDesc, tjmaxDesc, powerDesc, tdpDesc, speedDesc, fsbDesc, multDesc, vidDesc, coreCntDesc, cpuCntDesc} {
		ch <- d
	}
}

func (collector) Collect(ch chan<- prometheus.Metric) {
	d, err := readSharedMemory()
	if err != nil {
		log.Printf("scrape failed: %v", err)
		ch <- prometheus.MustNewConstMetric(upDesc, prometheus.GaugeValue, 0)
		return
	}
	g := func(desc *prometheus.Desc, v float64, labels ...string) {
		ch <- prometheus.MustNewConstMetric(desc, prometheus.GaugeValue, v, labels...)
	}

	g(upDesc, 1)
	g(infoDesc, 1, cstr(d.CPUName[:]))
	g(coreCntDesc, float64(d.CoreCnt))
	g(cpuCntDesc, float64(d.CPUCnt))
	g(speedDesc, float64(d.CPUSpeed))
	g(fsbDesc, float64(d.FSBSpeed))
	g(multDesc, float64(d.Multiplier))
	g(vidDesc, float64(d.VID))

	cpuCnt := d.CPUCnt
	if cpuCnt == 0 {
		cpuCnt = 1
	}
	hasExtra := d.StructVersion >= 2
	for cpu := uint32(0); cpu < cpuCnt && cpu < uint32(len(d.TjMax)); cpu++ {
		cpuLabel := fmt.Sprint(cpu)
		g(tjmaxDesc, float64(d.TjMax[cpu]), cpuLabel)
		if hasExtra && d.PowerSupported != 0 {
			g(powerDesc, float64(d.Power[cpu]), cpuLabel)
		}
		if hasExtra && d.TdpSupported != 0 {
			g(tdpDesc, float64(d.Tdp[cpu]), cpuLabel)
		}
		for core := uint32(0); core < d.CoreCnt; core++ {
			idx := cpu*d.CoreCnt + core
			if int(idx) >= len(d.Temp) {
				continue
			}
			coreLabel := fmt.Sprint(core)
			g(tempDesc, d.celsius(idx, cpu), cpuLabel, coreLabel)
			g(loadDesc, float64(d.Load[idx])/100.0, cpuLabel, coreLabel)
		}
	}
}

func main() {
	addr := flag.String("listen", ":9184", "address to listen on")
	flag.Parse()

	reg := prometheus.NewRegistry()
	reg.MustRegister(collector{})
	reg.MustRegister(prometheus.NewGoCollector())

	http.Handle("/metrics", promhttp.HandlerFor(reg, promhttp.HandlerOpts{}))
	http.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`<html><head><title>Core Temp Exporter</title></head>` +
			`<body><h1>Core Temp Exporter</h1><p><a href="/metrics">Metrics</a></p></body></html>`))
	})

	log.Printf("coretemp-exporter listening on %s (scrape /metrics)", *addr)
	log.Fatal(http.ListenAndServe(*addr, nil))
}
