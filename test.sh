#! /bin/bash

go build -o bin/tfh_scorer

touch sweep_ta.txt
touch sweep_tb.txt

./bin/tfh_scorer -u a -k a sweep_ta.txt sweep_tb.txt &

cat sweep_a.txt | pv -q -L 1000000 > sweep_ta.txt &
cat sweep_b.txt | pv -q -L 1000000 > sweep_tb.txt

kill -USR1 $(ps -a | grep tfh_scorer | awk '{print $1}')
