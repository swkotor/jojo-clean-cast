package sponsorblock

import "testing"

func seg(a, b float64) SponsorBlockResponse {
	return SponsorBlockResponse{Segment: []float64{a, b}}
}

func TestCalculateSkippedTimeMergesOverlaps(t *testing.T) {
	cases := []struct {
		name string
		in   []SponsorBlockResponse
		want float64
	}{
		{"empty", nil, 0},
		{"disjoint", []SponsorBlockResponse{seg(10, 20), seg(30, 35)}, 15},
		{"nested", []SponsorBlockResponse{seg(100, 200), seg(120, 150)}, 100},
		{"overlap", []SponsorBlockResponse{seg(0, 10), seg(5, 20)}, 20},
		{"out of order", []SponsorBlockResponse{seg(50, 60), seg(0, 10), seg(5, 12)}, 22},
		{"bad span ignored", []SponsorBlockResponse{seg(20, 10), seg(0, 5)}, 5},
	}
	for _, c := range cases {
		if got := calculateSkippedTime(c.in); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}
