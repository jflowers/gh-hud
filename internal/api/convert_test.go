package api

import (
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/jflowers/gh-hud/internal/dispatch"
	"github.com/jflowers/gh-hud/internal/hub"
	"github.com/jflowers/gh-hud/internal/model"
)

var (
	timeType  = reflect.TypeFor[time.Time]()
	durType   = reflect.TypeFor[time.Duration]()
	errorType = reflect.TypeFor[error]()
)

// filler sets every exported field of a value to a distinct non-zero
// value, recursively, so a round trip that drops any field shows up.
type filler struct{ n int }

func (f *filler) next() int { f.n++; return f.n }

func (f *filler) fill(v reflect.Value, path string, t *testing.T) {
	switch {
	case v.Type() == timeType:
		// UTC and whole microseconds: what the wire gives back.
		v.Set(reflect.ValueOf(time.Date(2026, 1, 1, 0, 0, f.next(), 0, time.UTC)))
		return
	case v.Type() == durType:
		v.SetInt(int64(f.next()) * int64(time.Second))
		return
	case v.Type() == errorType:
		v.Set(reflect.ValueOf(fmt.Errorf("err %d", f.next())))
		return
	}
	switch v.Kind() {
	case reflect.String:
		v.SetString(fmt.Sprintf("%s-%d", path, f.next()))
	case reflect.Bool:
		v.SetBool(true)
	case reflect.Int, reflect.Int64, reflect.Int32:
		v.SetInt(int64(f.next()))
	case reflect.Uint8, reflect.Uint32, reflect.Uint64:
		v.SetUint(uint64(f.next() % 100))
	case reflect.Pointer:
		v.Set(reflect.New(v.Type().Elem()))
		f.fill(v.Elem(), path, t)
	case reflect.Slice:
		s := reflect.MakeSlice(v.Type(), 2, 2)
		for i := range 2 {
			f.fill(s.Index(i), fmt.Sprintf("%s[%d]", path, i), t)
		}
		v.Set(s)
	case reflect.Map:
		m := reflect.MakeMap(v.Type())
		k := reflect.New(v.Type().Key()).Elem()
		f.fill(k, path+".key", t)
		e := reflect.New(v.Type().Elem()).Elem()
		f.fill(e, path+".val", t)
		m.SetMapIndex(k, e)
		v.Set(m)
	case reflect.Struct:
		for i := range v.NumField() {
			sf := v.Type().Field(i)
			if !sf.IsExported() {
				continue
			}
			f.fill(v.Field(i), path+"."+sf.Name, t)
		}
	default:
		t.Fatalf("filler: %s has unhandled kind %s (%s); teach the test", path, v.Kind(), v.Type())
	}
}

func filled[T any](t *testing.T) T {
	t.Helper()
	var v T
	(&filler{}).fill(reflect.ValueOf(&v).Elem(), reflect.TypeFor[T]().Name(), t)
	return v
}

// diff names the first differing field path, for a readable failure.
func diff(a, b reflect.Value, path string) string {
	if a.Type() == errorType || (a.Kind() == reflect.Interface && !a.IsNil() && !b.IsNil()) {
		if fmt.Sprint(a.Interface()) != fmt.Sprint(b.Interface()) {
			return path
		}
		return ""
	}
	switch a.Kind() {
	case reflect.Pointer:
		if a.IsNil() || b.IsNil() {
			if a.IsNil() != b.IsNil() {
				return path
			}
			return ""
		}
		return diff(a.Elem(), b.Elem(), path)
	case reflect.Struct:
		if a.Type() == timeType {
			if !a.Interface().(time.Time).Equal(b.Interface().(time.Time)) {
				return path
			}
			return ""
		}
		for i := range a.NumField() {
			if !a.Type().Field(i).IsExported() {
				continue
			}
			if d := diff(a.Field(i), b.Field(i), path+"."+a.Type().Field(i).Name); d != "" {
				return d
			}
		}
		return ""
	case reflect.Slice:
		if a.Len() != b.Len() {
			return fmt.Sprintf("%s (len %d vs %d)", path, a.Len(), b.Len())
		}
		for i := range a.Len() {
			if d := diff(a.Index(i), b.Index(i), fmt.Sprintf("%s[%d]", path, i)); d != "" {
				return d
			}
		}
		return ""
	case reflect.Map:
		if a.Len() != b.Len() {
			return path + " (size)"
		}
		for _, k := range a.MapKeys() {
			bv := b.MapIndex(k)
			if !bv.IsValid() {
				return fmt.Sprintf("%s[%v] missing", path, k)
			}
			if d := diff(a.MapIndex(k), bv, fmt.Sprintf("%s[%v]", path, k)); d != "" {
				return d
			}
		}
		return ""
	}
	if !reflect.DeepEqual(a.Interface(), b.Interface()) {
		return fmt.Sprintf("%s (%v vs %v)", path, a.Interface(), b.Interface())
	}
	return ""
}

// roundTrip also pushes the wire value through proto marshaling, so the
// test covers what a client really receives.
func roundTrip[T any, P proto.Message](t *testing.T, to func(T) P, from func(P) T) {
	t.Helper()
	in := filled[T](t)
	p := to(in)
	b, err := proto.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	p2 := p.ProtoReflect().New().Interface().(P)
	if err := proto.Unmarshal(b, p2); err != nil {
		t.Fatal(err)
	}
	out := from(p2)
	if d := diff(reflect.ValueOf(in), reflect.ValueOf(out), reflect.TypeFor[T]().Name()); d != "" {
		t.Errorf("%s does not survive the wire: %s\nmap it in convert.go (and proto/ghhud/v1/hub.proto, then make proto)",
			reflect.TypeFor[T]().Name(), d)
	}
}

func TestRoundTrip(t *testing.T) {
	t.Run("Snapshot", func(t *testing.T) { roundTrip(t, SnapshotToPB, SnapshotFromPB) })
	t.Run("Event", func(t *testing.T) { roundTrip(t, EventToPB, EventFromPB) })
	t.Run("MenuChoice", func(t *testing.T) { roundTrip(t, menuChoiceToPB, menuChoiceFromPB) })
}

// TestZeroValuesStayZero checks that empty state is not invented on the
// way back (nil stays nil, zero times stay zero).
func TestZeroValuesStayZero(t *testing.T) {
	if got := SnapshotFromPB(SnapshotToPB(hub.Snapshot{})); !reflect.DeepEqual(got, hub.Snapshot{}) {
		t.Errorf("empty snapshot came back as %+v", got)
	}
	r := RunFromPB(RunToPB(dispatch.Run{}))
	if r.FinishedAt != nil || !r.StartedAt.IsZero() {
		t.Errorf("zero run came back as %+v", r)
	}
}

// TestDispatchRecordKeepsPlacement checks the part of a dispatch record
// that goes on the wire.
func TestDispatchRecordKeepsPlacement(t *testing.T) {
	in := dispatch.Record{Task: dispatch.TaskCI, Ref: "a/b#1", Workspace: "b@oak", Machine: "lap1", Runner: "herdr", Dir: "/d", PID: 9}
	out := recordFromPB(recordToPB(in))
	in.PID = 0 // not sent
	if !reflect.DeepEqual(in, out) {
		t.Errorf("record = %+v, want %+v", out, in)
	}
}

func TestSectionErrorMessage(t *testing.T) {
	s := hub.Snapshot{Res: &model.FetchResult{
		User:   "me",
		Errors: []model.SectionError{{Section: "user", Err: errors.New("boom")}},
	}}
	got := SnapshotFromPB(SnapshotToPB(s))
	if e := got.Res.Errors[0]; e.Section != "user" || e.Error() != "user: boom" {
		t.Errorf("section error = %+v", e)
	}
}
