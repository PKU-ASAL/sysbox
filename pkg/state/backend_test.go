package state

import (
	"context"
	"testing"
)

func TestParseBackendURL_Local(t *testing.T) {
	b, err := ParseBackendURL("/tmp/state.json")
	if err != nil {
		t.Fatalf("ParseBackendURL: %v", err)
	}
	lb, ok := b.(*LocalBackend)
	if !ok {
		t.Fatalf("expected LocalBackend, got %T", b)
	}
	if lb.Path != "/tmp/state.json" {
		t.Errorf("Path = %q, want /tmp/state.json", lb.Path)
	}
}

func TestParseBackendURL_FileScheme(t *testing.T) {
	b, err := ParseBackendURL("file:///tmp/state.json")
	if err != nil {
		t.Fatalf("ParseBackendURL: %v", err)
	}
	lb, ok := b.(*LocalBackend)
	if !ok {
		t.Fatalf("expected LocalBackend, got %T", b)
	}
	if lb.Path != "/tmp/state.json" {
		t.Errorf("Path = %q, want /tmp/state.json", lb.Path)
	}
}

func TestParseBackendURL_HTTP(t *testing.T) {
	b, err := ParseBackendURL("https://myserver.com/state.json")
	if err != nil {
		t.Fatalf("ParseBackendURL: %v", err)
	}
	hb, ok := b.(*HTTPBackend)
	if !ok {
		t.Fatalf("expected HTTPBackend, got %T", b)
	}
	if hb.URL != "https://myserver.com/state.json" {
		t.Errorf("URL = %q", hb.URL)
	}
}

func TestParseBackendURL_S3(t *testing.T) {
	b, err := ParseBackendURL("s3://my-bucket/sysbox/state.json")
	if err != nil {
		t.Fatalf("ParseBackendURL: %v", err)
	}
	sb, ok := b.(*S3Backend)
	if !ok {
		t.Fatalf("expected S3Backend, got %T", b)
	}
	if sb.Bucket != "my-bucket" {
		t.Errorf("Bucket = %q, want my-bucket", sb.Bucket)
	}
	if sb.Key != "sysbox/state.json" {
		t.Errorf("Key = %q, want sysbox/state.json", sb.Key)
	}
}

func TestParseBackendURL_S3Invalid(t *testing.T) {
	_, err := ParseBackendURL("s3://bucket-without-key")
	if err == nil {
		t.Error("expected error for s3 URL without key")
	}
}

func TestParseBackendURL_SQLite(t *testing.T) {
	b, err := ParseBackendURL("sqlite:///tmp/sysbox.db?topology=mixed")
	if err != nil {
		t.Fatalf("ParseBackendURL: %v", err)
	}
	sb, ok := b.(*SQLiteBackend)
	if !ok {
		t.Fatalf("expected SQLiteBackend, got %T", b)
	}
	if sb.Path != "/tmp/sysbox.db" || sb.Topology != "mixed" {
		t.Fatalf("sqlite backend = %#v", sb)
	}
}

func TestParseBackendURL_Postgres(t *testing.T) {
	b, err := ParseBackendURL("postgres://user:pass@localhost/sysbox?topology=mixed")
	if err != nil {
		t.Fatalf("ParseBackendURL: %v", err)
	}
	pb, ok := b.(*PostgresBackend)
	if !ok {
		t.Fatalf("expected PostgresBackend, got %T", b)
	}
	if pb.Topology != "mixed" {
		t.Fatalf("Topology = %q", pb.Topology)
	}
}

func TestParseBackendURL_Unsupported(t *testing.T) {
	_, err := ParseBackendURL("ftp://host/path")
	if err == nil {
		t.Error("expected error for unsupported scheme")
	}
}

func TestLocalBackend_SaveLoad(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/state.json"
	b := &LocalBackend{Path: path}

	s := &State{Version: SchemaVersion, Resources: []Resource{
		{Type: "sysbox_node", Name: "test", Provider: "docker"},
	}}
	data, err := s.Marshal()
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if err := b.Save(nil, data); err != nil {
		t.Fatalf("Save: %v", err)
	}
	loaded, err := b.Load(nil)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded == nil {
		t.Fatal("Load returned nil")
	}
	s2, err := Unmarshal(loaded)
	if err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if len(s2.Resources) != 1 {
		t.Errorf("Resources = %d, want 1", len(s2.Resources))
	}
}

func TestLocalBackend_MetadataAndSnapshot(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/state.json"
	b := &LocalBackend{Path: path}
	ctx := context.Background()

	s := &State{Version: SchemaVersion, Resources: []Resource{{Type: "sysbox_network", Name: "dmz"}}}
	data, err := s.Marshal()
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if err := b.Save(ctx, data); err != nil {
		t.Fatalf("Save: %v", err)
	}
	meta, err := b.Metadata(ctx)
	if err != nil {
		t.Fatalf("Metadata: %v", err)
	}
	if meta.Backend != "local" || meta.Serial != 1 {
		t.Fatalf("metadata = %#v", meta)
	}
	snap, err := b.Snapshot(ctx, "test")
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if snap == nil || snap.ID == "" || snap.Size == 0 {
		t.Fatalf("snapshot = %#v", snap)
	}
	snaps, err := b.ListSnapshots(ctx)
	if err != nil {
		t.Fatalf("ListSnapshots: %v", err)
	}
	if len(snaps) != 1 {
		t.Fatalf("snapshots = %d, want 1", len(snaps))
	}
}
