package std

type comparableType interface {
	~int | ~int8 | ~int16 | ~int32 | ~int64 | ~uint | ~uint8 | ~uint16 | ~uint32 | ~uint64 | ~uintptr | ~float32 |
		~float64 | ~string
}

func Max[T comparableType](number T, numbers ...T) T {
	for _, value := range numbers {
		if number < value {
			number = value
		}
	}
	return number
}

func Min[T comparableType](number T, numbers ...T) T {
	for _, value := range numbers {
		if number > value {
			number = value
		}
	}
	return number
}

// BoolPtr returns a pointer to the given boolean value.
func BoolPtr(b bool) *bool {
	return &b
}
