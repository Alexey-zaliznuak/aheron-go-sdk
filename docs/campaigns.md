# Campaign integrations

`integration.Client.Campaigns` wraps the core campaign API. Configure
`CampaignOAuth` with the installation's provider, project and installation IDs.
The client requests `campaigns.read` or `campaigns.write` for audience `campaign`;
named triggers use audience `backend` and `campaigns.read`. A client is bound to
one project and rejects another project's ID before network I/O.

`Create` requires a stable UUID `CommandID` per create intent. Reuse the ID and
body after an uncertain response. Changed content needs a new command ID.
`Update`, `Launch` and `Prepare` carry the current campaign revision; a conflict
requires reloading before presenting a new action. Creation has a durable command
ledger that permits automatic write replay after a 401. `StartPrepared` is also
safe to replay; other writes are not automatically replayed.

## Prepare the audience before sending

`Prepare` starts a revision-checked immutable audience snapshot. With campaign-service
5bf2c16 and later, execution begins when the snapshot is durable: `snapshotting`
then `running`, or `done` when empty. It never waits for analytics membership,
ClickHouse, or an integration consumer. There is no fallback to `Launch` when
an older server does not support the endpoint.

Older servers used `preparing` and `awaiting_audience` plus `StartPrepared`.
The current worker resumes those stored states under the campaign lease while
preserving batches, cursors, ownership and execution idempotency keys. Cancelled
campaigns are never revived. Deploy this server change before removing any legacy
consumer that used to acknowledge audience preparation.

`StartPrepared` remains a compatibility call: it checks the immutable identity,
completed snapshot and exact count, and is safe to replay. New executionSnapshot
campaigns reject it. Do not introduce a new dependency on this acknowledgement;
new audience events have `preparationRequired: false`.

Campaign metadata is a JSON object. The platform supplies the authoritative
`campaignId` in execution metadata and separately in `ExecutionContext.CampaignID`.
Integrations must use the latter for attribution rather than trusting arbitrary
variables or metadata. A missing campaign ID means an ordinary scenario run.

`RequiredFilter` is combined with the audience filter after manual inclusions.
It therefore also applies to explicitly included subjects. CRM search and count
expose the same field so previews use the launch semantics. The installation CRM
client also supports bounded `GetSubjects` batches (200 IDs), remote variable
source discovery and lookup. Discovery/lookup use audience `backend` with
`crm.read`; the platform calls the declared source. Tokens never follow a
catalog-provided URL. The normal CRM endpoints use audience `crm`.

## Event contracts

The `campaign` package defines version 1 audience and Messenger delivery
observations. It centralizes stable IDs, canonical payload hashing and the fixed
64-bucket function. Sources enqueue observations in their existing transaction
and relay them through the shared outbox. Consumers must deduplicate on event ID
and reject a different payload under an existing ID.

The separate `executionevent` package defines generic integration-action
outcomes for every integration. Its event contains context/version, integration,
block, project/subject and optional campaign attribution. Execution does not
interpret Messenger statuses or its delivery IDs. Integrations consuming these
events own that mapping.

Send, dispatch and execution-resolution observations are independent. A failed
OAuth confirmation after a successful platform send must not erase that send.
Success here means platform API acceptance, not proof that the user read a message.

No separate source-history or repair mechanism is introduced. Ordinary outbox
retries and Kafka redelivery are supported; replay of external observations is
bounded by Kafka retention once the source outbox has drained. Campaign stage history belongs to analytics-service. Messenger legacy status
projections and their history are retired; delivery receipts and explicit new
stage observations remain operational.

Run `task test:campaigns` for contract and OAuth boundary checks. Platform endpoints,
OAuth audience configuration and corresponding service migrations must be deployed
before an integration starts using these APIs.
