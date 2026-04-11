package std

func ContainsSlice[T comparable](container []T, subset []T) bool {
	for _, s := range subset {
		found := false
		for _, c := range container {
			if s == c {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func ContainsElement[T comparable](container []T, element T) bool {
	for _, v := range container {
		if v == element {
			return true
		}
	}
	return false
}
