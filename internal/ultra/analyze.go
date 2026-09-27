package ultra

// Analyze returns the delta filter size (0 = none, 1..4) that UC2's multimedia
// detection (COMPINT.CPP Analyze) selects for data at level 4 or 5.
func Analyze(data []byte, level int) int {
	const num = 1450
	if (level != 4 && level != 5) || len(data) <= 1500 {
		return 0
	}
	sample := data[(len(data)-num)/2:][:num]
	best, bestVal := 0, uint32(1000)
	cost := func(freq []uint32, count int) uint32 {
		var lens [nLD]byte
		huffLengths(freq, maxBits, lens[:len(freq)])
		tot := uint32(0)
		for i := range 256 {
			tot += freq[i] * uint32(lens[i])
		}
		return tot * 100 / uint32(count)
	}
	var freq [nLD]uint32
	for _, c := range sample {
		freq[c]++
	}
	freq[0] = 0
	if v := cost(freq[:], num); v < bestVal-5 {
		best, bestVal = 0, v
	}
	var d [num]byte
	for n := 1; n <= 4; n++ {
		copy(d[:], sample)
		dl := NewDelta(n)
		dl.Encode(d[:])
		clear(freq[:])
		count := num
		for i, c := range d {
			if i >= 5 && c == 0 && d[i-1] == 0 && d[i-2] == 0 {
				count--
			} else {
				freq[c]++
			}
		}
		if count == 0 {
			continue
		}
		if v := cost(freq[:256], count); v < bestVal-5 {
			best, bestVal = n, v
		}
	}
	if best == 0 {
		return 0
	}
	// Confirm with trial compressions, as UC2 does: a quarter of the data at
	// method 1 for level 4, all of it at method 2 for level 5.
	trial, trialLevel := data, 2
	if level == 4 {
		trialLevel = 1
		trial = nil
		for i := 0; i < len(data); i += 2048 {
			trial = append(trial, data[i:min(i+512, len(data))]...)
		}
	}
	plain := len(Compress(trial, nil, trialLevel))
	dt := append([]byte(nil), trial...)
	dl := NewDelta(best)
	dl.Encode(dt)
	if len(Compress(dt, nil, trialLevel)) > plain {
		return 0
	}
	return best
}
