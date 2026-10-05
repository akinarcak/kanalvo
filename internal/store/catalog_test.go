package store_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"streamhub/internal/store"
	"streamhub/internal/testdb"
)

func TestCatalogListsAreScopedToTenant(t *testing.T) {
	s, ctx := testdb.New(t), context.Background()
	t1 := must(s.CreateTenant(ctx, "t1"))
	t2 := must(s.CreateTenant(ctx, "t2"))
	spor := must(s.CreateCategory(ctx, t1, "Spor"))
	haber := must(s.CreateCategory(ctx, t1, "Haber"))
	must(s.CreateCategory(ctx, t2, "Başka"))
	c1 := must(s.CreateChannel(ctx, t1, "Kanal 1", "s1"))
	c2 := must(s.CreateChannel(ctx, t1, "Kanal 2", "s2"))
	must(s.CreateChannel(ctx, t2, "Yabancı", "s3"))
	if err := s.SetChannelCategory(ctx, c1, &spor); err != nil {
		t.Fatal(err)
	}

	cats := must(s.CategoriesByTenant(ctx, t1))
	if len(cats) != 2 || cats[0].ID != spor || cats[0].Name != "Spor" || cats[1].ID != haber || cats[0].TenantID != t1 {
		t.Fatalf("beklenmeyen kategoriler: %+v", cats)
	}

	chans := must(s.ChannelsByTenant(ctx, t1))
	if len(chans) != 2 || chans[0].ID != c1 || chans[1].ID != c2 {
		t.Fatalf("beklenmeyen kanallar: %+v", chans)
	}
	if chans[0].CategoryID == nil || *chans[0].CategoryID != spor || chans[1].CategoryID != nil {
		t.Fatalf("kategori atamaları yanlış: %+v", chans)
	}
	if chans[0].TenantStatus != "active" || chans[0].CreatedAt.IsZero() || time.Since(chans[0].CreatedAt) > time.Hour {
		t.Fatalf("kanal alanları eksik: %+v", chans[0])
	}

	if got := must(s.ChannelsByTenant(ctx, t1+999)); len(got) != 0 {
		t.Fatalf("olmayan yayıncı için boş liste bekleniyordu: %+v", got)
	}
}

func TestSetChannelCategoryRejectsOtherTenantsCategory(t *testing.T) {
	s, ctx := testdb.New(t), context.Background()
	t1 := must(s.CreateTenant(ctx, "t1"))
	t2 := must(s.CreateTenant(ctx, "t2"))
	foreign := must(s.CreateCategory(ctx, t2, "Başka"))
	ch := must(s.CreateChannel(ctx, t1, "Kanal", "s1"))

	if err := s.SetChannelCategory(ctx, ch, &foreign); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("ErrNotFound bekleniyordu, gelen: %v", err)
	}
	if must(s.ChannelByID(ctx, ch)).CategoryID != nil {
		t.Fatal("başka yayıncının kategorisi atanmamalı")
	}
	missing := foreign + 999
	if err := s.SetChannelCategory(ctx, ch, &missing); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("olmayan kategori için ErrNotFound bekleniyordu, gelen: %v", err)
	}
}

func TestClearingAndDeletingCategory(t *testing.T) {
	s, ctx := testdb.New(t), context.Background()
	tid := must(s.CreateTenant(ctx, "t"))
	cat := must(s.CreateCategory(ctx, tid, "Spor"))
	ch := must(s.CreateChannel(ctx, tid, "Kanal", "s1"))
	if err := s.SetChannelCategory(ctx, ch, &cat); err != nil {
		t.Fatal(err)
	}
	if err := s.SetChannelCategory(ctx, ch, nil); err != nil {
		t.Fatal(err)
	}
	if must(s.ChannelByID(ctx, ch)).CategoryID != nil {
		t.Fatal("kategori kaldırılabilmeli")
	}

	if err := s.SetChannelCategory(ctx, ch, &cat); err != nil {
		t.Fatal(err)
	}
	other := must(s.CreateTenant(ctx, "t2"))
	if err := s.DeleteCategory(ctx, other, cat); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("başka yayıncı kategoriyi silememeli, gelen: %v", err)
	}
	if err := s.DeleteCategory(ctx, tid, cat); err != nil {
		t.Fatal(err)
	}
	if got := must(s.CategoriesByTenant(ctx, tid)); len(got) != 0 {
		t.Fatalf("kategori silinmeliydi: %+v", got)
	}
	if must(s.ChannelByID(ctx, ch)).CategoryID != nil {
		t.Fatal("kategorisi silinen kanal kategorisiz kalmalı")
	}
	if err := s.DeleteCategory(ctx, tid, cat); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("olmayan kategori için ErrNotFound bekleniyordu, gelen: %v", err)
	}
}

func TestViewerHasCreatedAt(t *testing.T) {
	s, ctx := testdb.New(t), context.Background()
	tid := must(s.CreateTenant(ctx, "t"))
	must(s.CreateViewer(ctx, tid, "ali", "pw", 1))
	v := must(s.ViewerByUsername(ctx, "ali"))
	if v.CreatedAt.IsZero() || time.Since(v.CreatedAt) > time.Hour {
		t.Fatalf("CreatedAt dolu olmalı: %v", v.CreatedAt)
	}
}
