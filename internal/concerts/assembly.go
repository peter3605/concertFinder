package concerts

import "encoding/json"

// AssembleByKey turns a slice of raw JSON concert blobs into a []Concert
// in the same order as the provided dedupKeys. Post-normalization
// (migration 0012) the snapshot stores dedup_keys separately from concert
// bodies; callers do a batched load of blobs and use this to reindex.
// Rows missing from the batch (e.g. pruned by the janitor) are silently
// skipped rather than returned as errors.
func AssembleByKey(dedupKeys []string, blobs [][]byte) ([]Concert, error) {
	byKey := make(map[string]Concert, len(blobs))
	for _, b := range blobs {
		var c Concert
		if err := json.Unmarshal(b, &c); err != nil {
			return nil, err
		}
		byKey[c.DedupKey] = c
	}
	out := make([]Concert, 0, len(dedupKeys))
	for _, k := range dedupKeys {
		if c, ok := byKey[k]; ok {
			out = append(out, c)
		}
	}
	return out, nil
}
