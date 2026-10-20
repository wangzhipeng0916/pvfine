package services

import "os"

// ensureConfigFile writes a template only when path does not exist. An existing
// file always wins, including when a save races the startup initializer.
func ensureConfigFile(path string, template []byte) (created bool, err error) {
	if _, err := os.Stat(path); err == nil {
		return false, nil
	} else if !os.IsNotExist(err) {
		return false, err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		if os.IsExist(err) {
			return false, nil
		}
		return false, err
	}
	if _, err := file.Write(template); err != nil {
		file.Close()
		return false, err
	}
	return true, file.Close()
}

// The templates below are empty documents, not copies of the built-in content.
// Built-in bookmarks, the default file set and built-in rules stay visible in
// their editors; copying them here would duplicate or pin entries (a same-id
// rule in the overlay replaces the built-in one forever).
const bookmarkConfigTemplate = `{
  "version": 1,
  "activeBookId": "builtin",
  "books": []
}
`

const fileSetConfigTemplate = `{
  "version": 1,
  "fileSets": [],
  "activeSetId": ""
}
`

const ruleOverlayTemplate = `{
  "version": 1,
  "rules": []
}
`
