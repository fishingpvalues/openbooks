import {
  Badge,
  Center,
  CopyButton,
  Group,
  Loader,
  Menu,
  Stack,
  Text,
  Tooltip
} from "@mantine/core";
import { showNotification } from "@mantine/notifications";
import { AnimatePresence, motion } from "motion/react";
import { ArrowClockwise, CheckCircle, ShieldCheck } from "phosphor-react";
import {
  useGetJobsQuery,
  useRetryJobMutation,
  useVerifyBookMutation,
  type DownloadJob
} from "../../state/api";
import { defaultAnimation } from "../../utils/animation";

// The download job log (v5.3.0): every POST /download the operator made,
// newest first, 256 cap. A completed job is the verify/retry handle -
// the file name is what POST /verify and the library listing key on.
export default function Jobs() {
  const { data, isLoading, isError } = useGetJobsQuery(null, {
    pollingInterval: 15_000
  });

  if (isLoading) {
    return (
      <Center>
        <Loader />
      </Center>
    );
  }

  if (isError) {
    return (
      <Center>
        <Text color="dimmed" size="sm">
          Jobs unavailable.
        </Text>
      </Center>
    );
  }

  if (data && data.length > 0) {
    return (
      <Stack gap="xs">
        <AnimatePresence mode="popLayout">
          {data.slice(0, 30).map((job) => (
            <motion.div {...defaultAnimation} key={job.id}>
              <JobCard key={job.id} job={job} />
            </motion.div>
          ))}
        </AnimatePresence>
      </Stack>
    );
  }

  return (
    <Center>
      <Text color="dimmed" size="sm">
        No download jobs yet.
      </Text>
    </Center>
  );
}

function statusBadge(job: DownloadJob) {
  switch (job.status) {
    case "completed":
      return (
        <Badge color="green" radius="sm" size="sm" variant="light">
          completed
        </Badge>
      );
    case "failed":
      return (
        <Badge color="red" radius="sm" size="sm" variant="light">
          failed
        </Badge>
      );
    case "downloading":
      return (
        <Badge color="blue" radius="sm" size="sm" variant="light">
          downloading
        </Badge>
      );
    default:
      return (
        <Badge color="gray" radius="sm" size="sm" variant="light">
          requested
        </Badge>
      );
  }
}

interface JobCardProps {
  job: DownloadJob;
}

function JobCard({ job }: JobCardProps) {
  const [retryJob, { isLoading: retrying }] = useRetryJobMutation();
  const [verifyBook, { isLoading: verifying }] = useVerifyBookMutation();
  const retryable =
    job.status === "failed" || job.status === "completed";
  const verifiable = job.status === "completed" && !!job.fileName;

  const onVerify = () => {
    if (!job.fileName) return;
    verifyBook({ fileName: job.fileName, recompute: true }).then(
      (res) => {
        const r = res.data;
        if (!r) return;
        const color =
          r.status === "ok" ? "green" : r.status === "mismatch" ? "red" : "yellow";
        showNotification({
          title: `Verify: ${r.status}`,
          message: `${r.fileName}${
            r.sha256 ? ` -> ${r.sha256.slice(0, 16)}...` : ""
          }${r.detail ? ` (${r.detail})` : ""}`,
          color: color
        });
      },
      (err: any) =>
        showNotification({
          title: "Verify failed",
          message: err?.data?.error ?? String(err),
          color: "red"
        })
    );
  };

  return (
    <Menu shadow="md">
      <Menu.Target>
        <Tooltip
          label={`${job.book}${
            job.fileName ? ` -> ${job.fileName}` : ""
          }${job.failedDetail ? ` (${job.failedDetail})` : ""}`}
          openDelay={1_000}>
          <button
            style={{
              display: "flex",
              alignItems: "center",
              gap: 6,
              width: "100%",
              cursor: "pointer",
              border: "none",
              background: "none",
              padding: 0,
              textAlign: "start"
            }}>
            {statusBadge(job)}
            <Text
              size="sm"
              lineClamp={1}
              style={{
                flex: 1,
                overflow: "hidden",
                textOverflow: "ellipsis",
                whiteSpace: "nowrap"
              }}>
              {job.fileName ?? job.book}
            </Text>
          </button>
        </Tooltip>
      </Menu.Target>

      <Menu.Dropdown>
        <Menu.Item disabled>
          <div>
            <Text size="xs">
              {new Date(job.requestedAt).toLocaleString("en-US")}
              {job.completedAt
                ? ` -> ${new Date(job.completedAt).toLocaleString("en-US")}`
                : ""}
            </Text>
            {job.retries > 0 && (
              <Text size="xs" c="dimmed">
                retried {job.retries}x
              </Text>
            )}
            {job.sha256 && (
              <Group gap="xs" wrap="nowrap">
                <Text size="xs" c="dimmed" style={{ maxWidth: 160 }}>
                  {job.sha256.slice(0, 16)}...
                </Text>
                <CopyButton value={job.sha256} timeout={1200}>
                  {({ copied }) =>
                    copied ? (
                      <CheckCircle size={12} color="green" />
                    ) : (
                      <Text size="xs" c="dimmed">
                        copy
                      </Text>
                    )
                  }
                </CopyButton>
              </Group>
            )}
          </div>
        </Menu.Item>

        {retryable && (
          <Menu.Item
            leftSection={
              retrying ? <Loader size={12} type="dots" color="gray" /> : <ArrowClockwise size={16} weight="bold" />
            }
            onClick={() =>
              retryJob(job.id).then(
                (res) =>
                  showNotification({
                    title: "Re-requested",
                    message: res.data?.detail ?? "sent",
                    color: "brand"
                  }),
                (err: any) =>
                  showNotification({
                    title: "Retry refused",
                    message: err?.data?.error ?? String(err),
                    color: "red"
                  })
              )
            }>
            Re-request book
          </Menu.Item>
        )}

        {verifiable && (
          <Menu.Item
            leftSection={
              verifying ? <Loader size={12} type="dots" color="gray" /> : <ShieldCheck size={16} weight="bold" />
            }
            onClick={onVerify}>
            Verify sha256
          </Menu.Item>
        )}

        {job.status === "failed" && (
          <Text size="xs" c="dimmed" style={{ padding: "4px 8px" }}>
            {job.failedDetail}
          </Text>
        )}
      </Menu.Dropdown>
    </Menu>
  );
}
