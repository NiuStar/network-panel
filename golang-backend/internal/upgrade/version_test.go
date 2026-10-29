package upgrade

import (
	"context"
	"testing"
)

func TestReleaseVersionMustIncreaseNumerically(t *testing.T) {
	for _, current := range []string{"2.0.0.31", "2.0.0.32", "2.0.1", "3.0.0", "not-a-version"} {
		t.Run(current, func(t *testing.T) {
			c, _, _, _ := fixture(t)
			if _, e := stageRelease(context.Background(), Options{HTTPClient: c, CurrentVersion: current}, t.TempDir(), "amd64"); e == nil {
				t.Fatal("non-increasing/invalid version accepted")
			}
		})
	}
	for _, current := range []string{"2.0.0.9", "2.0.0.30", "1.99.99.999"} {
		t.Run(current, func(t *testing.T) {
			c, _, _, _ := fixture(t)
			if _, e := stageRelease(context.Background(), Options{HTTPClient: c, CurrentVersion: current}, t.TempDir(), "amd64"); e != nil {
				t.Fatal(e)
			}
		})
	}
}
