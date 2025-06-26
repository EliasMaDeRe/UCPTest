#include <bits/stdc++.h>
using namespace std;
func main(){

input := "";
output := "";
seen = make(map[char]int);

getline(cin,input);
for(int i = 0; i < input.size(); ++i){
    if (!seen[input[i]]++){
        output.push_back(input[i]);
    }
}
print(output);
}
