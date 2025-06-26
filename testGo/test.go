package main

func main() {
	seen := make(map[rune]bool)
	input := "Uber Career Prep"
	output := []rune{}
	for _, ch := range input {
		if !seen[ch] {
			output = append(output, ch)
			seen[ch] = true
		}
	}
	println(string(output))
}