package storage

import (
	"fmt"
	"path/filepath"
	"testing"

	"github.com/ishwarchandra-dev/onegate/internal/domain"
)

func openMigrated(t *testing.T) *Store {
	t.Helper()
	s, err := OpenTemp()
	if err != nil {
		t.Fatalf("OpenTemp: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	if err := s.Migrate(); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	return s
}

func TestMigrateFresh(t *testing.T) {
	s := openMigrated(t)
	v, err := s.SchemaVersion()
	if err != nil {
		t.Fatalf("SchemaVersion: %v", err)
	}
	if v != CurrentVersion() {
		t.Fatalf("fresh db: want version %d, got %d", CurrentVersion(), v)
	}
}

func TestMigrateIdempotent(t *testing.T) {
	s := openMigrated(t)
	if err := s.Migrate(); err != nil {
		t.Fatalf("second Migrate: %v", err)
	}
	v, _ := s.SchemaVersion()
	if v != CurrentVersion() {
		t.Fatalf("re-migrate changed version: %d", v)
	}
}

func TestMigrateUpgradePath(t *testing.T) {
	// Open a db, migrate, close, reopen and migrate again: an "upgrade".
	dir := t.TempDir()
	path := filepath.Join(dir, "db.sqlite")

	s1, err := Open(path)
	if err != nil {
		t.Fatalf("open1: %v", err)
	}
	if err := s1.Migrate(); err != nil {
		t.Fatalf("migrate1: %v", err)
	}
	s1.Close()

	s2, err := Open(path)
	if err != nil {
		t.Fatalf("open2: %v", err)
	}
	defer s2.Close()
	if err := s2.Migrate(); err != nil {
		t.Fatalf("migrate2 (upgrade): %v", err)
	}
	if v, _ := s2.SchemaVersion(); v != CurrentVersion() {
		t.Fatalf("upgrade version mismatch: %d", v)
	}
}

func TestProviderCRUD(t *testing.T) {
	s := openMigrated(t)
	repo := s.Providers()

	rec := ProviderRecord{
		Provider: domain.Provider{
			Name:     "OpenAI prod",
			Protocol: domain.ProtocolOpenAI,
			BaseURL:  "https://api.openai.com",
			Enabled:  true,
		},
		APIKeyEnc: []byte{0x01, 0x02},
	}
	if err := repo.Upsert(&rec); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if rec.ID == "" {
		t.Fatal("upsert should assign an id")
	}

	got, err := repo.Get(rec.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Name != "OpenAI prod" || got.Protocol != domain.ProtocolOpenAI ||
		string(got.APIKeyEnc) != "\x01\x02" || !got.Enabled {
		t.Fatalf("round-trip mismatch: %+v", got)
	}

	// update
	got.BaseURL = "https://api.openai.com/v2"
	if err := repo.Upsert(&got); err != nil {
		t.Fatalf("update: %v", err)
	}
	got2, _ := repo.Get(rec.ID)
	if got2.BaseURL != "https://api.openai.com/v2" {
		t.Fatalf("update not applied: %s", got2.BaseURL)
	}

	list, err := repo.List()
	if err != nil || len(list) != 1 {
		t.Fatalf("list: %v, len=%d", err, len(list))
	}

	if err := repo.Delete(rec.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := repo.Get(rec.ID); err != ErrNotFound {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
	if err := repo.Delete(rec.ID); err != ErrNotFound {
		t.Fatalf("double delete should be ErrNotFound, got %v", err)
	}
}

func TestModelWithTargetsRoundTrip(t *testing.T) {
	s := openMigrated(t)
	providers := s.Providers()
	// model_targets has FKs to providers; create one first.
	if err := providers.Upsert(&ProviderRecord{Provider: domain.Provider{
		Name: "p1", Protocol: domain.ProtocolOpenAI, Enabled: true,
	}}); err != nil {
		t.Fatal(err)
	}
	plist, _ := providers.List()
	provID := plist[0].ID

	mrepo := s.Models()
	m := domain.Model{
		ID:           "gpt-4o",
		Aliases:      []string{"gpt4o", "4o"},
		Capabilities: domain.ModelCapabilities{Tools: true, Stream: true},
		Targets: []domain.ModelTarget{
			{ProviderID: provID, ProviderModel: "gpt-4o-2024-08-06", Weight: 2, Position: 0},
			{ProviderID: provID, ProviderModel: "gpt-4o-mini", Weight: 1, Position: 1},
		},
	}
	if err := mrepo.Upsert(m); err != nil {
		t.Fatalf("upsert model: %v", err)
	}

	got, err := mrepo.Get("gpt-4o")
	if err != nil {
		t.Fatalf("get model: %v", err)
	}
	if len(got.Aliases) != 2 || got.Aliases[0] != "gpt4o" {
		t.Fatalf("aliases lost: %v", got.Aliases)
	}
	if !got.Capabilities.Tools || !got.Capabilities.Stream {
		t.Fatalf("capabilities lost: %+v", got.Capabilities)
	}
	if len(got.Targets) != 2 ||
		got.Targets[0].ProviderModel != "gpt-4o-2024-08-06" ||
		got.Targets[1].Weight != 1 {
		t.Fatalf("targets lost: %+v", got.Targets)
	}

	// replace targets via re-upsert
	m.Targets = m.Targets[:1]
	if err := mrepo.Upsert(m); err != nil {
		t.Fatalf("re-upsert: %v", err)
	}
	got, _ = mrepo.Get("gpt-4o")
	if len(got.Targets) != 1 {
		t.Fatalf("target replacement failed: %+v", got.Targets)
	}

	// deleting the provider cascades model_targets
	if err := providers.Delete(provID); err != nil {
		t.Fatal(err)
	}
	got, _ = mrepo.Get("gpt-4o")
	if len(got.Targets) != 0 {
		t.Fatalf("cascade delete failed: %+v", got.Targets)
	}
}

func TestVirtualKeyRoundTripAndHashLookup(t *testing.T) {
	s := openMigrated(t)
	repo := s.VirtualKeys()

	k := domain.VirtualKey{
		Name:    "ci-key",
		Prefix:  "ogk-lt4x",
		KeyHash: "$argon2id$hashdata",
		Scopes:  domain.KeyScopes{AllowedModels: []string{"gpt-4o"}},
		Limits:  domain.KeyLimits{RPM: 60, MaxSpendUSDMicros: 500},
	}
	created, err := repo.Create(k)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	got, err := repo.Get(created.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.KeyHash != k.KeyHash || len(got.Scopes.AllowedModels) != 1 ||
		got.Limits.RPM != 60 || got.Status != domain.KeyActive {
		t.Fatalf("round-trip mismatch: %+v", got)
	}

	byHash, err := repo.GetByHash(k.KeyHash)
	if err != nil || byHash.ID != created.ID {
		t.Fatalf("hash lookup failed: %v %+v", err, byHash)
	}

	if err := repo.UpdateStatus(created.ID, domain.KeyRevoked); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	got, _ = repo.Get(created.ID)
	if got.Status != domain.KeyRevoked {
		t.Fatalf("status not updated: %s", got.Status)
	}
}

func TestRequestCursorPagination(t *testing.T) {
	s := openMigrated(t)
	repo := s.Requests()

	// insert 25 requests with distinct timestamps
	const total = 25
	for i := 0; i < total; i++ {
		rec := domain.RequestRecord{
			TraceID:        fmt.Sprintf("tr-%02d", i),
			VirtualKeyID:   "vkey_1",
			ModelRequested: "gpt-4o",
			Status:         domain.RequestSuccess,
			TotalTokens:    int64(100 + i),
			CreatedMS:      int64(1_000_000 + i),
		}
		if err := repo.Insert(rec); err != nil {
			t.Fatalf("insert %d: %v", i, err)
		}
	}

	// walk pages of 10 newest-first
	var got []domain.RequestRecord
	cursor := ""
	pages := 0
	for {
		page, err := repo.ListByTime("vkey_1", 10, cursor)
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		got = append(got, page.Items...)
		pages++
		if page.NextCur == "" {
			break
		}
		cursor = page.NextCur
		if pages > 10 {
			t.Fatal("pagination did not terminate")
		}
	}
	if len(got) != total {
		t.Fatalf("want %d items across pages, got %d", total, len(got))
	}
	// strictly descending by (created_ms, id)
	for i := 1; i < len(got); i++ {
		if got[i-1].CreatedMS < got[i].CreatedMS {
			t.Fatalf("ordering violated at %d: %d then %d",
				i, got[i-1].CreatedMS, got[i].CreatedMS)
		}
	}

	// keyset stability: no duplicates
	seen := map[string]bool{}
	for _, r := range got {
		if seen[r.ID] {
			t.Fatalf("duplicate item in pagination: %s", r.ID)
		}
		seen[r.ID] = true
	}
}

func TestRequestFilterByVKey(t *testing.T) {
	s := openMigrated(t)
	repo := s.Requests()
	for i := 0; i < 5; i++ {
		_ = repo.Insert(domain.RequestRecord{
			TraceID: fmt.Sprintf("a-%d", i), VirtualKeyID: "keyA",
			Status: domain.RequestSuccess, CreatedMS: int64(2000 + i),
		})
		_ = repo.Insert(domain.RequestRecord{
			TraceID: fmt.Sprintf("b-%d", i), VirtualKeyID: "keyB",
			Status: domain.RequestSuccess, CreatedMS: int64(3000 + i),
		})
	}
	page, err := repo.ListByTime("keyA", 50, "")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(page.Items) != 5 {
		t.Fatalf("want 5 items for keyA, got %d", len(page.Items))
	}
	for _, r := range page.Items {
		if r.VirtualKeyID != "keyA" {
			t.Fatalf("filter leaked other keys: %+v", r)
		}
	}
}
