FROM scratch

ADD https://curl.haxx.se/ca/cacert.pem /etc/ssl/certs/cacert.pem
ADD flowlog-exporter /flowlog-exporter

CMD [ "/flowlog-exporter" ]
ENTRYPOINT [ "/flowlog-exporter" ]