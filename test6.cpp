#include <bits/stdc++.h>
using namespace std;
int main(){

string input;
string output;
unordered_map<char,int> seen;

getline(cin,input);
for(int i = 0; i < input.size(); ++i){
    if (!seen[input[i]]++){
        output.push_back(input[i]);
    }
}
cout<<"@test"<<endl;
cout<<output;

return 0;}
