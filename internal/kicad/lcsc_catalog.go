package kicad

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// The catalogue cache keeps the library record of an LCSC number (MPN,
// manufacturer, package, description) so offline commands — the BOM sheets of
// sch-build — can show it. It is written by the commands that ask the
// catalogue anyway (kicad lcsc --verify, sch-build --bom-catalog).

func catalogCachePath(lcsc string) string {
	return filepath.Join(CacheRoot(), "kicad-lcsc-catalog", lcsc+".json")
}

// CatalogCached returns the cached record of lcsc.
func CatalogCached(lcsc string) (EasyEDAPart, bool) {
	if !ValidLCSC(lcsc) {
		return EasyEDAPart{}, false
	}
	b, err := os.ReadFile(catalogCachePath(lcsc))
	if err != nil {
		return EasyEDAPart{}, false
	}
	var p EasyEDAPart
	if json.Unmarshal(b, &p) != nil || p.LCSC != lcsc {
		return EasyEDAPart{}, false
	}
	return p, true
}

// CatalogStore caches one record (written whole: temp file, then rename).
func CatalogStore(p EasyEDAPart) error {
	if !ValidLCSC(p.LCSC) {
		return nil
	}
	path := catalogCachePath(p.LCSC)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := json.Marshal(p)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), p.LCSC+".*.tmp")
	if err != nil {
		return err
	}
	_, werr := f.Write(b)
	cerr := f.Close()
	if werr != nil || cerr != nil {
		os.Remove(f.Name())
		if werr != nil {
			return werr
		}
		return cerr
	}
	if err := os.Rename(f.Name(), path); err != nil {
		os.Remove(f.Name())
		return err
	}
	return nil
}
