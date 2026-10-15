package bundlepatch

import (
	"bytes"
	"github.com/gabstv/go-bsdiff/pkg/bspatch"
	"github.com/stretchr/testify/require"
	"math/rand"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestObjectKeyCompatibilityAndIsolation(t *testing.T) {
	r := Record{Version: 2, Platform: "ios", BaseID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", PatchHash: strings.Repeat("a", 64)}
	key, err := ObjectKey(r)
	require.NoError(t, err)
	require.Equal(t, "bundle-patches/ios/"+r.BaseID+"/"+r.PatchHash+".bsdiff", key)
	for _, mutate := range []func(*Record){
		func(r *Record) { r.Platform = "android" },
		func(r *Record) { r.BaseID = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb" },
		func(r *Record) { r.PatchHash = strings.Repeat("b", 64) },
	} {
		changed := r
		mutate(&changed)
		other, err := ObjectKey(changed)
		require.NoError(t, err)
		require.NotEqual(t, key, other)
	}
	for _, mutate := range []func(*Record){
		func(r *Record) { r.Version = 3 },
		func(r *Record) { r.Platform = "../ios" },
		func(r *Record) { r.BaseID = "../base" },
		func(r *Record) { r.PatchHash = strings.Repeat("A", 64) },
		func(r *Record) { r.PatchHash = strings.Repeat("a", 62) },
		func(r *Record) { r.PatchHash = "../../other" },
	} {
		invalid := r
		mutate(&invalid)
		_, err := ObjectKey(invalid)
		require.Error(t, err)
	}
	r.Version = 1
	r.PatchHash = ""
	legacy, err := ObjectKey(r)
	require.NoError(t, err)
	require.Equal(t, "bundle-patches/ios/"+r.BaseID+".bsdiff", legacy)
}

func TestPrepareAndApply(t *testing.T) {
	base := make([]byte, 32000)
	rand.New(rand.NewSource(7)).Read(base)
	target := bytes.Clone(base)
	copy(target[4000:], []byte("A real changed bundle string"))
	record, patch, err := Prepare(base, target, Record{BaseID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", TargetID: "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb", Runtime: "test.1", Platform: "ios", Asset: "bundle.hbc"})
	require.NoError(t, err)
	require.Less(t, record.PatchBytes, record.GzipBytes)
	actual, err := bspatch.Bytes(base, patch)
	require.NoError(t, err)
	require.Equal(t, target, actual)
	w := httptest.NewRecorder()
	Serve(w, record, patch)
	require.Equal(t, 226, w.Code)
	require.Equal(t, "bsdiff", w.Header().Get("IM"))
	require.Equal(t, record.BaseID, w.Header().Get("Expo-Base-Update-ID"))
	require.Equal(t, "private, no-store", w.Header().Get("Cache-Control"))
}
func TestDoesNotOfferLargerPatch(t *testing.T) {
	_, _, err := Prepare([]byte("one"), []byte("two"), Record{BaseID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", TargetID: "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb", Runtime: "test.1", Platform: "ios", Asset: "bundle.hbc"})
	require.ErrorIs(t, err, ErrNotSmaller)
}
func TestNegotiation(t *testing.T) {
	for _, tc := range []struct {
		value string
		want  bool
	}{{"bsdiff", true}, {"other, BSDIFF", true}, {"", false}, {"bsdiff;q=0", false}, {"not-bsdiff", false}} {
		r := httptest.NewRequest("GET", "/assets", nil)
		r.Header.Set("A-IM", tc.value)
		require.Equal(t, tc.want, AcceptsPatch(r))
	}
}
func TestRejectsInvalidInputs(t *testing.T) {
	_, err := Key("ios", "../../secrets")
	require.Error(t, err)
	_, err = Key("web", "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa")
	require.Error(t, err)
	_, err = ReadBounded(bytes.NewReader(make([]byte, 11)), 10)
	require.Error(t, err)
	_, _, err = Prepare(nil, []byte("x"), Record{})
	require.Error(t, err)
}
