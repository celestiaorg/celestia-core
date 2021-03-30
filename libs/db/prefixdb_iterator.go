package db

// IteratePrefix is a convenience function for iterating over a key domain
// restricted by prefix.
func IteratePrefix(db DB, prefix []byte) (Iterator, error) {
	var start, end []byte
	if len(prefix) == 0 {
		start = nil
		end = nil
	} else {
		start = cp(prefix)
		end = increment(prefix)
	}
	itr, err := db.Iterator(start, end)
	if err != nil {
		return nil, err
	}
	return itr, nil
}
