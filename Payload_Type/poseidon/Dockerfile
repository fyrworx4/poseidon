FROM itsafeaturemythic/mythic_go_macos:latest

WORKDIR /Mythic/

COPY [".", "."]

RUN pwd

RUN ls -la

RUN ls -la /MythicMeta/MythicContainer

RUN make build

CMD make run
