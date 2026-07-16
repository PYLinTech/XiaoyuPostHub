//go:build integration

package systemsetting_test

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/PYLinTech/XiaoyuPostHub/backend/systemsetting"
	"github.com/PYLinTech/XiaoyuPostHub/backend/test/dbtest"
)

func TestMain(m *testing.M) {
	dbtest.SetupOrExit(m)
	code := m.Run()
	dbtest.Teardown()
	os.Exit(code)
}

func TestEnsureDefaults(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	repo := systemsetting.NewRepo(dbtest.Queries())
	if err := repo.EnsureDefaults(ctx); err != nil {
		t.Fatalf("EnsureDefaults: %v", err)
	}
	settings, err := repo.Get(ctx)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if settings.SiteName != systemsetting.DefaultSiteName {
		t.Errorf("SiteName = %q, want %q", settings.SiteName, systemsetting.DefaultSiteName)
	}
	if settings.StoragePath != systemsetting.DefaultStoragePath {
		t.Errorf("StoragePath = %q, want %q", settings.StoragePath, systemsetting.DefaultStoragePath)
	}
}

func TestEnsureDefaultsDoesNotOverwriteAndUpdateNormalizes(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	repo := systemsetting.NewRepo(dbtest.Queries())
	if err := repo.EnsureDefaults(ctx); err != nil {
		t.Fatalf("EnsureDefaults: %v", err)
	}

	updated, err := repo.Update(ctx, "  My Post Hub  ", "/srv/xph/uploads/")
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if updated.SiteName != "My Post Hub" {
		t.Errorf("SiteName = %q, want My Post Hub", updated.SiteName)
	}
	if updated.StoragePath != "/srv/xph/uploads" {
		t.Errorf("StoragePath = %q, want /srv/xph/uploads", updated.StoragePath)
	}

	if err := repo.EnsureDefaults(ctx); err != nil {
		t.Fatalf("EnsureDefaults second call: %v", err)
	}
	afterEnsure, err := repo.Get(ctx)
	if err != nil {
		t.Fatalf("Get after second EnsureDefaults: %v", err)
	}
	if afterEnsure.SiteName != updated.SiteName || afterEnsure.StoragePath != updated.StoragePath {
		t.Errorf("EnsureDefaults 覆盖了已有配置：got %+v, want %+v", afterEnsure, updated)
	}
}

func TestUpdateValidation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	repo := systemsetting.NewRepo(dbtest.Queries())
	if _, err := repo.Update(ctx, "  ", "/data/uploads"); !errors.Is(err, systemsetting.ErrSiteNameBlank) {
		t.Errorf("空站点名称错误 = %v, want ErrSiteNameBlank", err)
	}
	if _, err := repo.Update(ctx, "XiaoyuPostHub", "relative/uploads"); !errors.Is(err, systemsetting.ErrStoragePathInvalid) {
		t.Errorf("相对路径错误 = %v, want ErrStoragePathInvalid", err)
	}
}
