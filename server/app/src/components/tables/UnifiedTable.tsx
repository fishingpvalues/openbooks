import { Badge, Button, ScrollArea, Table, Text, Tooltip } from "@mantine/core";
import { ArrowDown, Link as LinkIcon } from "phosphor-react";
import { useState } from "react";
import { useSelector } from "react-redux";
import type { UnifiedResult } from "../../state/api";
import { sendDownload } from "../../state/stateSlice";
import { RootState, useAppDispatch } from "../../state/store";
import { useTableStyles } from "./styles";

// The unified multi-source result set (v5.2.0). Rows are normalized
// across legs: the IRC leg exposes a BookID (the "!"-prefixed DCC line,
// fetched through the shared session) and the Prowlarr leg exposes a
// URL (magnet / download link, handed to the operator's torrent client).
// The two verbs are therefore different per row.
interface UnifiedTableProps {
  results: UnifiedResult[];
}

export default function UnifiedTable({ results }: UnifiedTableProps) {
  const { classes } = useTableStyles();

  if (results.length === 0) {
    return (
      <Text size="sm" c="dimmed" style={{ padding: 16 }}>
        No results from the selected sources.
      </Text>
    );
  }

  return (
    <ScrollArea
      viewportRef={undefined}
      className={classes.container}
      type="hover"
      scrollbarSize={6}
      styles={{ thumb: { ["&::before"]: { minWidth: 4 } } }}
      offsetScrollbars={false}
      style={{ maxHeight: 480 }}>
      <Table highlightOnHover verticalSpacing="sm" fz="xs">
        <thead className={classes.head}>
          <tr>
            <th className={classes.headerCell}>Source</th>
            <th className={classes.headerCell}>Title</th>
            <th className={classes.headerCell}>Author</th>
            <th className={classes.headerCell}>Format</th>
            <th className={classes.headerCell}>Size</th>
            <th className={classes.headerCell}>Dedupe</th>
            <th className={classes.headerCell}>Fetch</th>
          </tr>
        </thead>
        <tbody>
          {results.map((r, i) => (
            <tr key={i}>
              <td>
                <Text lineClamp={1} color="dark">
                  <Badge
                    color={r.source === "irc" ? "blue" : "grape"}
                    variant="light"
                    size="sm">
                    {r.source}
                  </Badge>
                </Text>
              </td>
              <td>
                <Text lineClamp={1} color="dark">
                  {r.title}
                </Text>
              </td>
              <td>
                <Text lineClamp={1} color="dimmed">
                  {r.author ?? "—"}
                </Text>
              </td>
              <td>
                <Text lineClamp={1} color="dark">
                  {r.format ?? "—"}
                </Text>
              </td>
              <td>
                <Text lineClamp={1} color="dimmed">
                  {r.size || "—"}
                </Text>
              </td>
              <td>
                {r.dedupGroup && r.dedupGroup > 1 ? (
                  <Tooltip
                    label={`${r.dedupGroup} sources offer the same book`}>
                    <Badge color="yellow" variant="light" size="sm">
                      x{r.dedupGroup}
                    </Badge>
                  </Tooltip>
                ) : (
                  <Text color="dimmed">—</Text>
                )}
              </td>
              <td>
                {r.bookId ? (
                  <DccDownloadButton book={r.bookId} />
                ) : r.url ? (
                  <MagnetButton url={r.url} />
                ) : (
                  <Text size="xs" c="dimmed">
                    —
                  </Text>
                )}
              </td>
            </tr>
          ))}
        </tbody>
      </Table>
    </ScrollArea>
  );
}

function DccDownloadButton({ book }: { book: string }) {
  const dispatch = useAppDispatch();
  const [clicked, setClicked] = useState(false);
  const isInFlight = useSelector((state: RootState) =>
    state.state.inFlightDownloads.includes(book)
  );

  const onClick = () => {
    if (clicked) return;
    dispatch(sendDownload(book));
    setClicked(true);
  };

  return (
    <Button
      size="xs"
      radius="sm"
      leftSection={<ArrowDown weight="bold" size={12} />}
      onClick={onClick}
      style={{ fontWeight: "normal" }}>
      {isInFlight ? "Sent" : "Download"}
    </Button>
  );
}

function MagnetButton({ url }: { url: string }) {
  return (
    <Tooltip label={url} openDelay={500}>
      <Button
        size="xs"
        radius="sm"
        leftSection={<LinkIcon weight="bold" size={12} />}
        onClick={() => window.open(url, "_blank", "noopener")}
        style={{ fontWeight: "normal" }}>
        Magnet
      </Button>
    </Tooltip>
  );
}
