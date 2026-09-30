package config

import (
        "context"
        "testing"
        "time"
)

func TestReloadSwapsConfigAndNotifies(t *testing.T) {
        dir := t.TempDir()
        path := writeFile(t, dir, "onegate.json", `{"port": 1111}`)
        t.Setenv("ONEGATE_HOST", "")
        t.Setenv("ONEGATE_PORT", "")
        t.Setenv("ONEGATE_DATA_DIR", "")
        t.Setenv("ONEGATE_LOG_LEVEL", "")

        initial, err := Load(LoadOptions{Path: path})
        if err != nil {
                t.Fatalf("Load: %v", err)
        }
        w := NewWatcher(path, initial, 100, nil)
        defer w.Stop()

        events := make(chan ChangeEvent, 4)
        w.Subscribe(events)

        // change the file
        writeFile(t, dir, "onegate.json", `{"port": 2222}`)

        if _, err := w.ReloadNow(); err != nil {
                t.Fatalf("ReloadNow: %v", err)
        }
        if got := w.Current().Port; got != 2222 {
                t.Fatalf("current config not swapped: port=%d", got)
        }

        select {
        case evt := <-events:
                if evt.Old.Port != 1111 || evt.New.Port != 2222 {
                        t.Fatalf("bad event: %+v", evt)
                }
                if len(evt.ChangedFields) != 1 || evt.ChangedFields[0] != "port" {
                        t.Fatalf("unexpected changed fields: %v", evt.ChangedFields)
                }
        case <-time.After(time.Second):
                t.Fatal("no change event received")
        }
}

func TestReloadFailureKeepsLastGood(t *testing.T) {
        dir := t.TempDir()
        path := writeFile(t, dir, "onegate.json", `{"port": 1111}`)
        t.Setenv("ONEGATE_HOST", "")
        t.Setenv("ONEGATE_PORT", "")
        t.Setenv("ONEGATE_DATA_DIR", "")
        t.Setenv("ONEGATE_LOG_LEVEL", "")

        initial, err := Load(LoadOptions{Path: path})
        if err != nil {
                t.Fatalf("Load: %v", err)
        }
        w := NewWatcher(path, initial, 100, nil)
        defer w.Stop()

        // break the file
        writeFile(t, dir, "onegate.json", `{"port": `)
        if _, err := w.ReloadNow(); err == nil {
                t.Fatal("expected reload error")
        }
        if got := w.Current().Port; got != 1111 {
                t.Fatalf("last-good config must survive, got port=%d", got)
        }
}

func TestWatcherDetectsFileChangeWithinPollWindow(t *testing.T) {
        dir := t.TempDir()
        path := writeFile(t, dir, "onegate.json", `{"port": 3333}`)
        t.Setenv("ONEGATE_HOST", "")
        t.Setenv("ONEGATE_PORT", "")
        t.Setenv("ONEGATE_DATA_DIR", "")
        t.Setenv("ONEGATE_LOG_LEVEL", "")

        initial, err := Load(LoadOptions{Path: path})
        if err != nil {
                t.Fatalf("Load: %v", err)
        }
        w := NewWatcher(path, initial, 150, nil)
        defer w.Stop()

        done := make(chan struct{})
        ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
        defer cancel()
        w.Start(ctx)

        // mutate after the watcher is running
        time.Sleep(50 * time.Millisecond)
        writeFile(t, dir, "onegate.json", `{"port": 4444}`)

        go func() {
                for {
                        if w.Current().Port == 4444 {
                                close(done)
                                return
                        }
                        select {
                        case <-ctx.Done():
                                return
                        case <-time.After(20 * time.Millisecond):
                        }
                }
        }()

        select {
        case <-done:
                // observed the swap
        case <-ctx.Done():
                t.Fatal("watcher did not pick up file change in time")
        }
}
