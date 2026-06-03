Now Check:
http://localhost:9000/dashboard?id=okorach-oss_sonar-tools&codeScope=overall
and 
https://sc-staging.io/project/background_tasks?id=open-digital-society-1_okorach-oss_sonar-tools
There is a delta in number of issues. Ruff external issues I think.

Instruction:
Verify ---> Fix ---> Verify ---> Fix  ....recursively. 

Stop condition:
Number of issues match exactly 1:1 between SonarQube Server and SonarCloud for the `Sonar Tools` project