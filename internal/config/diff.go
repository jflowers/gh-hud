package config

import (
	"fmt"
	"reflect"
	"strings"
)

// Changes lists the settings that differ between a and b, by TOML name:
// "max_concurrent 2→3" for a plain value, "supervisor.enabled false→true"
// inside a table, and "machines changed" for lists and maps. Warnings are
// not settings and are left out.
func Changes(a, b *Config) []string {
	var out []string
	diffStruct(reflect.ValueOf(a).Elem(), reflect.ValueOf(b).Elem(), "", &out)
	return out
}

func diffStruct(a, b reflect.Value, prefix string, out *[]string) {
	t := a.Type()
	for i := range t.NumField() {
		f := t.Field(i)
		name, _, _ := strings.Cut(f.Tag.Get("toml"), ",")
		if name == "" || name == "-" || !f.IsExported() {
			continue
		}
		diffValue(a.Field(i), b.Field(i), prefix+name, out)
	}
}

func diffValue(a, b reflect.Value, key string, out *[]string) {
	if reflect.DeepEqual(a.Interface(), b.Interface()) {
		return
	}
	switch a.Kind() {
	case reflect.Struct:
		diffStruct(a, b, key+".", out)
	case reflect.Pointer:
		if a.IsNil() || b.IsNil() {
			*out = append(*out, key+" changed")
			return
		}
		diffValue(a.Elem(), b.Elem(), key, out)
	case reflect.String, reflect.Bool, reflect.Int, reflect.Int64, reflect.Float64:
		*out = append(*out, fmt.Sprintf("%s %s→%s", key, show(a), show(b)))
	default:
		*out = append(*out, key+" changed")
	}
}

// show renders a value briefly: long strings (commands) are cut short.
func show(v reflect.Value) string {
	if v.Kind() == reflect.String {
		s := v.String()
		if s == "" {
			return `""`
		}
		if r := []rune(s); len(r) > 32 {
			return string(r[:31]) + "…"
		}
		return s
	}
	return fmt.Sprint(v.Interface())
}
