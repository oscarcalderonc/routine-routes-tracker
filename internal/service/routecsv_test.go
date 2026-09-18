package service

import (
	"bytes"
	"strings"
	"testing"

	"github.com/oscarcalderonc/routine-routes-tracker/internal/domain"
)

func TestRouteCSV_RoundTrip(t *testing.T) {
	route := domain.Template{
		Name: "Daily route, school run",
		Waypoints: []domain.Waypoint{
			{Seq: 0, Label: "End of street", Lat: 13.692912345678, Lon: -89.218234567891, RadiusM: 25},
			{Seq: 1, Label: `Bridge "north"`, Lat: 13.7, Lon: -89.21, RadiusM: 22.5, Optional: true},
			{Seq: 2, Label: "Gate", Lat: 13.71, Lon: -89.2, RadiusM: 30},
		},
	}

	var buf bytes.Buffer
	if err := WriteRouteCSV(&buf, route); err != nil {
		t.Fatalf("WriteRouteCSV returned %v", err)
	}
	name, wps, err := ReadRouteCSV(&buf)
	if err != nil {
		t.Fatalf("ReadRouteCSV returned %v", err)
	}
	if name != route.Name {
		t.Errorf("name = %q, want %q", name, route.Name)
	}
	if len(wps) != len(route.Waypoints) {
		t.Fatalf("got %d waypoints, want %d", len(wps), len(route.Waypoints))
	}
	for i, want := range route.Waypoints {
		got := wps[i]
		if got.Seq != want.Seq || got.Label != want.Label || got.Lat != want.Lat ||
			got.Lon != want.Lon || got.RadiusM != want.RadiusM || got.Optional != want.Optional {
			t.Errorf("waypoint %d = %+v, want %+v", i, got, want)
		}
	}
}

func TestReadRouteCSV_OrdersBySeqAndFindsColumnsByName(t *testing.T) {
	// Reordered rows and columns, a byte-order mark and a blank optional flag,
	// as a spreadsheet might save them.
	in := "\ufefflabel,lat,lon,radius_m,optional,seq,route\n" +
		"Second,13.7,-89.2,25,,2,Mine\n" +
		"First,13.6,-89.1,25,TRUE,1,Mine\n"
	name, wps, err := ReadRouteCSV(strings.NewReader(in))
	if err != nil {
		t.Fatalf("ReadRouteCSV returned %v", err)
	}
	if name != "Mine" {
		t.Errorf("name = %q, want Mine", name)
	}
	if wps[0].Label != "First" || wps[1].Label != "Second" {
		t.Errorf("order = %s, %s; want First, Second", wps[0].Label, wps[1].Label)
	}
	if wps[0].Seq != 0 || wps[1].Seq != 1 {
		t.Errorf("seqs = %d, %d; want 0, 1", wps[0].Seq, wps[1].Seq)
	}
	if !wps[0].Optional || wps[1].Optional {
		t.Errorf("optional = %t, %t; want true, false", wps[0].Optional, wps[1].Optional)
	}
}

func TestReadRouteCSV_Rejects(t *testing.T) {
	const header = "route,seq,label,lat,lon,radius_m,optional\n"
	const ok = "R,1,A,13.6,-89.1,25,false\n"

	tests := []struct {
		name string
		in   string
		want string
	}{
		{"empty file", "", "empty"},
		{"missing column", "route,seq,label,lat,lon\nR,1,A,13.6,-89.1\n", `missing column "radius_m"`},
		{"too few waypoints", header + ok, "at least two"},
		{"duplicate seq", header + ok + "R,1,B,13.7,-89.2,25,false\n", "more than once"},
		{"bad latitude", header + ok + "R,2,B,113.7,-89.2,25,false\n", "lat must be"},
		{"bad radius", header + ok + "R,2,B,13.7,-89.2,0,false\n", "radius_m must be"},
		{"no label", header + ok + "R,2,,13.7,-89.2,25,false\n", "label is required"},
		{"bad flag", header + ok + "R,2,B,13.7,-89.2,25,maybe\n", "optional must be"},
		{"short row", header + ok + "R,2,B\n", "line 3"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := ReadRouteCSV(strings.NewReader(tt.in))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("ReadRouteCSV error = %v, want one mentioning %q", err, tt.want)
			}
		})
	}
}
