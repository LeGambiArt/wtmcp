package highlighter

import (
	"testing"
)

func TestParseColor(t *testing.T) {
	tests := []struct {
		name    string
		hex     string
		wantR   float64
		wantG   float64
		wantB   float64
		wantErr bool
	}{
		{
			name:  "valid purple-red",
			hex:   "#D73A49",
			wantR: 0.843137,
			wantG: 0.227451,
			wantB: 0.286275,
		},
		{
			name:  "valid dark blue",
			hex:   "#032F62",
			wantR: 0.011765,
			wantG: 0.184314,
			wantB: 0.384314,
		},
		{
			name:  "valid black",
			hex:   "#000000",
			wantR: 0.0,
			wantG: 0.0,
			wantB: 0.0,
		},
		{
			name:  "valid white",
			hex:   "#FFFFFF",
			wantR: 1.0,
			wantG: 1.0,
			wantB: 1.0,
		},
		{
			name:    "invalid format",
			hex:     "D73A49",
			wantErr: true,
		},
		{
			name:    "invalid length",
			hex:     "#D73A4",
			wantErr: true,
		},
		{
			name:    "invalid characters",
			hex:     "#GGGGGG",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rgb, err := ParseColor(tt.hex)
			if tt.wantErr {
				if err == nil {
					t.Errorf("ParseColor() expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Errorf("ParseColor() unexpected error: %v", err)
				return
			}
			// Allow small floating point differences
			if abs(rgb.Red-tt.wantR) > 0.001 {
				t.Errorf("ParseColor() Red = %v, want %v", rgb.Red, tt.wantR)
			}
			if abs(rgb.Green-tt.wantG) > 0.001 {
				t.Errorf("ParseColor() Green = %v, want %v", rgb.Green, tt.wantG)
			}
			if abs(rgb.Blue-tt.wantB) > 0.001 {
				t.Errorf("ParseColor() Blue = %v, want %v", rgb.Blue, tt.wantB)
			}
		})
	}
}

func abs(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}
