// +build race

package ipld

func init() {
	raceDetectorActive = true
}
