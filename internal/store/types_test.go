package store

import (
	"context"
	"testing"
)

func TestTypeMetaUpsertAndRead(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()

	got, err := s.TypeMetas(ctx, []int64{34, 587})
	if err != nil || len(got) != 0 {
		t.Fatalf("empty cache = %v, %v", got, err)
	}
	trit := TypeMeta{TypeID: 34, Name: "Tritanium", GroupID: 18, GroupName: "Mineral",
		CategoryID: 4, CategoryName: "Material", Volume: 0.01, PackagedVolume: 0.005}
	rifter := TypeMeta{TypeID: 587, Name: "Rifter", GroupID: 25, GroupName: "Frigate",
		CategoryID: 6, CategoryName: "Ship", Volume: 27289}
	for _, m := range []TypeMeta{trit, rifter} {
		if err := s.UpsertTypeMeta(ctx, m); err != nil {
			t.Fatalf("UpsertTypeMeta(%d): %v", m.TypeID, err)
		}
	}
	got, err = s.TypeMetas(ctx, []int64{34, 999})
	if err != nil || len(got) != 1 || got[34] != trit {
		t.Fatalf("TypeMetas = %+v, %v", got, err)
	}

	trit.Name = "Tritanium II"
	if err := s.UpsertTypeMeta(ctx, trit); err != nil {
		t.Fatal(err)
	}
	got, _ = s.TypeMetas(ctx, []int64{34, 587})
	if len(got) != 2 || got[34].Name != "Tritanium II" || got[587] != rifter {
		t.Fatalf("after overwrite = %+v", got)
	}
	if got, err := s.TypeMetas(ctx, nil); err != nil || len(got) != 0 {
		t.Fatalf("no ids = %v, %v", got, err)
	}
}
