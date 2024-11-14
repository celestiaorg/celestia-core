package load

import (
	"fmt"
	"log"
	"time"

	"gonum.org/v1/plot"
	"gonum.org/v1/plot/font"
	"gonum.org/v1/plot/plotter"
	plotutil "gonum.org/v1/plot/plotutil"
	"gonum.org/v1/plot/vg"
)

type Transit struct {
	SendTime    time.Time `json:"send_time"`
	ReceiveTime time.Time `json:"receive_time"`
	Size        int       `json:"size"`
	Channel     byte      `json:"channel"`
}

func (t Transit) Table() string {
	return "transit"
}

func VizBandwidth(filename string, data []Transit) {

	if len(data) == 0 {
		fmt.Println("No data to visualize")
		return
	}
	// Create a new plot
	p := plot.New()
	p.Title.Text = "Channel Bandwidth Comparison"
	p.X.Label.Text = "Time elapsed (seconds)"
	p.Y.Label.Text = "Bandwidth (MB/second)"

	pts := make(plotter.XYs, len(data))
	timeCursor := data[0].ReceiveTime
	for i, msg := range data {
		elapsed := msg.ReceiveTime.Sub(timeCursor)
		pts[i].X = float64(elapsed.Milliseconds()) // Convert time to a float64
		pts[i].Y = float64(msg.Size)
	}

	// Separate data by channel
	plotters := initPlotters(data)
	cursors := findStarts(data)
	starts := findStarts(data)
	for _, msg := range data {
		cursor := cursors[msg.Channel]
		start := starts[msg.Channel]
		elapsed := msg.ReceiveTime.Sub(cursor)
		totalElapsed := msg.ReceiveTime.Sub(start)
		cursors[msg.Channel] = msg.ReceiveTime
		pt := plotter.XY{X: float64(totalElapsed.Seconds()), Y: bandwidth(elapsed, msg.Size)}
		plotters[msg.Channel] = append(plotters[msg.Channel], pt)
	}

	for ch, points := range plotters {
		line, err := plotter.NewLine(points)
		if err != nil {
			log.Panic(err)
		}
		line.Color = plotutil.Color(int(ch))
		p.Legend.Add(fmt.Sprintf("Channel %X (priority=%2d)", ch, priorities[ch]), line)
		p.Add(line)
	}

	p.Legend.Left = false
	p.Legend.Top = true

	// Save the plot to a PNG file
	if err := p.Save(8*vg.Inch, 4*vg.Inch, filename); err != nil {
		log.Panic(err)
	}
}

func findStarts(channelData []Transit) map[byte]time.Time {
	starts := make(map[byte]time.Time)
	for _, msg := range channelData {
		if _, ok := starts[msg.Channel]; !ok {
			starts[msg.Channel] = msg.ReceiveTime
		}
	}
	return starts
}

func findEnds(channelData []Transit) map[byte]time.Time {
	ends := make(map[byte]time.Time)
	for _, msg := range channelData {
		ends[msg.Channel] = msg.ReceiveTime
	}
	return ends
}

func initPlotters(data []Transit) map[byte]plotter.XYs {
	plotters := make(map[byte]plotter.XYs)
	for _, d := range data {
		if _, ok := plotters[d.Channel]; !ok {
			plotters[d.Channel] = make(plotter.XYs, 0)
		}
	}
	return plotters
}

func bandwidth(elapsed time.Duration, size int) float64 {
	if elapsed.Nanoseconds() == 0 {
		return 0
	}
	MBSize := float64(size) / 1000000
	return MBSize / float64(elapsed.Seconds())
}

func VizTotalBandwidth(filename string, data []Transit) {
	// Create a new plot
	p := plot.New()
	p.Title.Text = "Total Bandwidth Used by Each Channel"
	p.Y.Label.Text = "Bandwidth (MB/sec)"
	p.X.Label.Text = "Channel"
	p.X.Tick.Marker = NilTicks{}

	totalBytesUsed := make(map[byte]int)
	starts := findStarts(data)
	ends := findEnds(data)
	for _, msg := range data {
		totalBytesUsed[msg.Channel] += msg.Size
	}

	total := 0.0
	bandwidthByChannel := make(plotter.Values, 0, len(totalBytesUsed))
	labels := plotter.XYLabels{}
	counter := 0
	for ch, bytes := range totalBytesUsed {
		start := starts[ch]
		end := ends[ch]
		elapsed := end.Sub(start)
		b := bandwidth(elapsed, bytes)
		total += b
		bandwidthByChannel = append(bandwidthByChannel, b)
		labels.Labels = append(labels.Labels, fmt.Sprintf("Channel %X", ch))
		labels.XYs = append(labels.XYs, plotter.XY{X: float64(counter), Y: b + 0.1})
		counter++
	}

	fmt.Println("Total bandwidth used: ", total)

	barWidth := float64(100) / float64(len(totalBytesUsed))

	// Create a bar chart
	bars, err := plotter.NewBarChart(plotter.Values(bandwidthByChannel), vg.Points(float64(barWidth)))
	if err != nil {
		log.Panic(err)
	}
	p.Add(bars)

	// Add labels to the bars
	barLabels, err := plotter.NewLabels(labels)
	if err != nil {
		log.Panic(err)
	}
	p.Add(barLabels)

	// Save the plot to a PNG file
	if err := p.Save(font.Length(len(totalBytesUsed)+1)*vg.Inch, 4*vg.Inch, filename); err != nil {
		log.Panic(err)
	}
}

type NilTicks struct{}

func (NilTicks) Ticks(min, max float64) []plot.Tick {
	return []plot.Tick{} // No ticks.
}

func averageLatency(data []Transit) map[byte]float64 {
	latencies := make(map[byte]float64)
	counts := make(map[byte]int)
	for _, msg := range data {
		latencies[msg.Channel] += float64(msg.ReceiveTime.Sub(msg.SendTime).Milliseconds())
		counts[msg.Channel]++
	}
	for ch, latency := range latencies {
		latencies[ch] = latency / float64(counts[ch])
	}
	return latencies
}
