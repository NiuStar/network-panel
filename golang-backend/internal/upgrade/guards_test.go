package upgrade

import (
	"archive/zip"
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestSQLiteDirectoryRejectedBeforeStop(t *testing.T) {
	o, _, stops := runFixture(t)
	os.Remove(o.DatabasePath)
	os.Mkdir(o.DatabasePath, 0700)
	s, e := Run(context.Background(), o)
	if e == nil || *stops != 0 {
		t.Fatalf("non-file database touched service: %+v %v stops=%d", s, e, *stops)
	}
}
func TestConfiguredLimitsCannotRelaxSafetyCeilings(t *testing.T) {
	base := limits(Limits{})
	got := limits(Limits{AssetBytes: base.AssetBytes + 1, TotalBytes: base.TotalBytes + 1, ZipBytes: base.ZipBytes + 1, ZipEntries: base.ZipEntries + 1})
	if got != base {
		t.Fatalf("relaxed safety ceilings: %+v", got)
	}
}
func TestZipDotEntryRejected(t *testing.T) {
	src := filepath.Join(t.TempDir(), "dot.zip")
	os.WriteFile(src, makeZipEntries(t, []zip.FileHeader{{Name: "./"}}), 0600)
	count := 0
	var size int64
	if e := unzip(src, filepath.Join(t.TempDir(), "out"), false, limits(Limits{}), &count, &size); e == nil {
		t.Fatal("accepted dot entry")
	}
}
