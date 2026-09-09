package gpx

import (
	"errors"
	"strings"
	"testing"
)

const maxTestBytes = 1 << 20

func TestParse_BasicTrack(t *testing.T) {
	const doc = `<?xml version="1.0"?>
<gpx version="1.1" xmlns="http://www.topografix.com/GPX/1/1">
 <trk><trkseg>
  <trkpt lat="52.2297" lon="21.0122"><ele>110</ele><time>2026-09-09T06:30:00Z</time></trkpt>
  <trkpt lat="52.2307" lon="21.0132"><ele>111</ele><time>2026-09-09T06:30:05Z</time></trkpt>
 </trkseg></trk>
</gpx>`

	track, err := Parse(strings.NewReader(doc), maxTestBytes)
	if err != nil {
		t.Fatalf("Parse returned %v", err)
	}
	if len(track.Points) != 2 {
		t.Fatalf("got %d points, want 2", len(track.Points))
	}
	if !track.Points[0].SegmentStart {
		t.Error("the first point should open a segment")
	}
	if track.Points[0].Lat != 52.2297 {
		t.Errorf("latitude = %v, want 52.2297", track.Points[0].Lat)
	}
	if got := track.Points[1].Time.Format("15:04:05"); got != "06:30:05" {
		t.Errorf("second timestamp = %s, want 06:30:05", got)
	}
}

func TestParse_PreservesSegmentBoundaries(t *testing.T) {
	const doc = `<gpx><trk>
 <trkseg>
  <trkpt lat="52.20" lon="21.00"><time>2026-09-09T06:30:00Z</time></trkpt>
  <trkpt lat="52.21" lon="21.00"><time>2026-09-09T06:31:00Z</time></trkpt>
 </trkseg>
 <trkseg>
  <trkpt lat="52.22" lon="21.00"><time>2026-09-09T06:45:00Z</time></trkpt>
 </trkseg>
</trk></gpx>`

	track, err := Parse(strings.NewReader(doc), maxTestBytes)
	if err != nil {
		t.Fatalf("Parse returned %v", err)
	}
	if len(track.Points) != 3 {
		t.Fatalf("got %d points, want 3", len(track.Points))
	}
	if !track.Points[2].SegmentStart {
		t.Error("the point after a recording pause should open a segment")
	}
	if track.Points[1].SegmentStart {
		t.Error("a mid-segment point should not open a segment")
	}
}

func TestParse_DropsImpreciseAndImpossibleFixes(t *testing.T) {
	// The third fix reports a hopeless HDOP; the fourth would imply well over
	// 150 km/h. Both must be discarded.
	const doc = `<gpx><trk><trkseg>
  <trkpt lat="52.2000" lon="21.0000"><time>2026-09-09T06:30:00Z</time><hdop>1.2</hdop></trkpt>
  <trkpt lat="52.2010" lon="21.0000"><time>2026-09-09T06:30:10Z</time><hdop>1.4</hdop></trkpt>
  <trkpt lat="52.2020" lon="21.0000"><time>2026-09-09T06:30:20Z</time><hdop>19</hdop></trkpt>
  <trkpt lat="52.6000" lon="21.0000"><time>2026-09-09T06:30:30Z</time><hdop>1.1</hdop></trkpt>
  <trkpt lat="52.2030" lon="21.0000"><time>2026-09-09T06:30:40Z</time><hdop>1.0</hdop></trkpt>
</trkseg></trk></gpx>`

	track, err := Parse(strings.NewReader(doc), maxTestBytes)
	if err != nil {
		t.Fatalf("Parse returned %v", err)
	}
	if len(track.Points) != 3 {
		t.Fatalf("got %d points, want 3 (two should be filtered)", len(track.Points))
	}
	if track.RawCount != 5 {
		t.Errorf("RawCount = %d, want 5", track.RawCount)
	}
	for _, p := range track.Points {
		if p.Lat > 52.5 {
			t.Error("the teleporting fix survived filtering")
		}
	}
}

func TestParse_ReadsAccuracyExtension(t *testing.T) {
	const doc = `<gpx><trk><trkseg>
  <trkpt lat="52.20" lon="21.00"><time>2026-09-09T06:30:00Z</time>
    <extensions><speed>12.0</speed><horizontalAccuracy>4.5</horizontalAccuracy></extensions>
  </trkpt>
  <trkpt lat="52.21" lon="21.00"><time>2026-09-09T06:31:00Z</time>
    <extensions><horizontalAccuracy>90.0</horizontalAccuracy></extensions>
  </trkpt>
</trkseg></trk></gpx>`

	track, err := Parse(strings.NewReader(doc), maxTestBytes)
	if err != nil {
		t.Fatalf("Parse returned %v", err)
	}
	if len(track.Points) != 1 {
		t.Fatalf("got %d points, want 1 (the 90 m fix should be dropped)", len(track.Points))
	}
	if track.Points[0].Accuracy != 4.5 {
		t.Errorf("Accuracy = %v, want 4.5", track.Points[0].Accuracy)
	}
}

func TestParse_Rejects(t *testing.T) {
	tests := []struct {
		name string
		doc  string
		want error
	}{
		{
			name: "doctype",
			doc:  `<!DOCTYPE gpx [<!ENTITY a "boom">]><gpx></gpx>`,
			want: ErrDoctype,
		},
		{
			name: "no points",
			doc:  `<gpx><trk><trkseg></trkseg></trk></gpx>`,
			want: ErrNoPoints,
		},
		{
			name: "points without timestamps",
			doc:  `<gpx><trk><trkseg><trkpt lat="52.2" lon="21.0"/></trkseg></trk></gpx>`,
			want: ErrNoTimestamps,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := Parse(strings.NewReader(tt.doc), maxTestBytes); !errors.Is(err, tt.want) {
				t.Errorf("Parse returned %v, want %v", err, tt.want)
			}
		})
	}
}

func TestParse_RejectsOversizedInput(t *testing.T) {
	if _, err := Parse(strings.NewReader(strings.Repeat("x", 200)), 100); err == nil {
		t.Error("Parse accepted input exceeding the byte limit")
	}
}
