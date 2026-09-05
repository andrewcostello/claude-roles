FROM golang:1.26.6@sha256:0d1d3a794be25f809dd2cb3160d8c73276c4056a9f8242a138e908ddeee7b6b6
WORKDIR /grading
COPY test.sh grader.py /tests/
RUN chmod -R go-w /tests && chmod 755 /tests/test.sh
