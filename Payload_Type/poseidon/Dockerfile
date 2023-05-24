FROM itsafeaturemythic/mythic_go_macos:latest

WORKDIR /Mythic/

COPY [".", "."]

RUN pwd

RUN ls

RUN make build

CMD make run
