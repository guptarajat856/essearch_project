 # Use alpine
FROM alpine:3.7

RUN apk update && apk add curl

LABEL maintainer="Rajat Gupta <Rajat.Gupta@netapp.com>"

ADD esproject ./

ENTRYPOINT ["/esproject"]
