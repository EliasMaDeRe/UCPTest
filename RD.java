import java.util.Scanner;
import java.util.Set;
import java.util.HashSet;

public class RD {
    public static void main(String[] args) {

        // Scanner is used for input, similar to C++'s cin
        Scanner scanner = new Scanner(System.in);

        // Read the entire line of input
        String input = scanner.nextLine();

        // StringBuilder is the efficient way to build strings in a loop in Java
        StringBuilder output = new StringBuilder();
        
        // A HashSet is the direct equivalent of C++'s unordered_set.
        // It provides O(1) average time complexity for add and contains operations.
        Set<Character> seen = new HashSet<>();

        // Loop through each character of the input string
        for (int i = 0; i < input.length(); i++) {
            char currentChar = input.charAt(i);
            
            // The add() method returns true if the character was not already in the set.
            // This perfectly replicates the logic of the C++ code.
            if (seen.add(currentChar)) {
                output.append(currentChar);
            }
        }

        // Print the final string without a newline, like the C++ cout
        System.out.print(output.toString());

        // It's good practice to close the scanner to release system resources
        scanner.close();
    }
}