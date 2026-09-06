#!/usr/bin/env bash
set +x
set -euo pipefail

mongod --replSet rs0 --bind_ip_all --port 27017 --dbpath /data/db \
  --keyFile /run/mongo-auth/keyfile --auth \
  --logpath /proc/1/fd/1 --logappend &
mongod_pid="$!"

if [[ "${HOSTNAME}" == "mongo-0" ]]; then
  for host in mongo-0.mongo-cluster mongo-1.mongo-cluster mongo-2.mongo-cluster; do
    until mongosh --quiet --host "${host}" --eval 'db.adminCommand({ping: 1})' >/dev/null 2>&1; do
      sleep 1
    done
  done
  # The localhost exception permits replica-set initialization and creation of
  # the first user. Secrets are read by mongosh from its environment, never argv.
  mongosh --quiet --eval 'rs.initiate({
    _id: "rs0", members: [
      {_id: 0, host: "mongo-0.mongo-cluster:27017", priority: 2},
      {_id: 1, host: "mongo-1.mongo-cluster:27017"},
      {_id: 2, host: "mongo-2.mongo-cluster:27017"}
    ]
  })' >/dev/null 2>&1
  until mongosh --quiet --eval 'quit(db.hello().isWritablePrimary ? 0 : 1)' >/dev/null 2>&1; do
    sleep 1
  done
  # hello and ping need no database roles. Authentication is still performed
  # by the driver when credentials are configured.
  mongosh --quiet --eval 'db.getSiblingDB("admin").createUser({
    user: process.env.MONGO_TEST_USERNAME,
    pwd: process.env.MONGO_TEST_PASSWORD,
    roles: []
  })' >/dev/null 2>&1
fi

# Mark every member ready only after its local server can authenticate the
# real test user. Replica-set user replication may lag behind initial election.
until mongosh --quiet --nodb --eval '
  const connection = new Mongo("mongodb://" +
    encodeURIComponent(process.env.MONGO_TEST_USERNAME) + ":" +
    encodeURIComponent(process.env.MONGO_TEST_PASSWORD) +
    "@localhost:27017/?directConnection=true&authSource=admin");
  const status = connection.getDB("admin").runCommand({connectionStatus: 1});
  quit(status.authInfo.authenticatedUsers.some(
    u => u.user === process.env.MONGO_TEST_USERNAME) ? 0 : 1);
' >/dev/null 2>&1; do
  sleep 1
done
touch /tmp/mongo-auth-ready
wait "${mongod_pid}"
