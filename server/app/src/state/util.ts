import { showNotification } from "@mantine/notifications";
import { getToken } from "./token";
import { Notification, NotificationType } from "./messages";

export const getWebsocketURL = (): URL => {
  const websocketURL = new URL(window.location.href + "ws");
  if (websocketURL.protocol.startsWith("https")) {
    websocketURL.protocol = websocketURL.protocol.replace("https", "wss");
  } else {
    websocketURL.protocol = websocketURL.protocol.replace("http", "ws");
  }

  if (import.meta.env.DEV) {
    websocketURL.port = "5228";
  }

  const token = getToken();
  if (token) {
    websocketURL.searchParams.set("token", token);
  }

  return websocketURL;
};

export const getApiURL = (): URL => {
  const apiURL = new URL(window.location.href);
  if (import.meta.env.DEV) {
    apiURL.port = "5228";
  }

  return apiURL;
};

export const displayNotification = ({
  appearance = NotificationType.NOTIFY,
  title,
  detail
}: Notification) => {
  switch (appearance) {
    case NotificationType.NOTIFY:
      showNotification({
        color: "brand",
        title: title,
        message: detail
      });
      break;
    case NotificationType.SUCCESS:
      showNotification({
        color: "green",
        title: title,
        message: detail
      });

      break;
    case NotificationType.WARNING:
      showNotification({
        color: "yellow",
        title: title,
        message: detail
      });
      break;
    case NotificationType.DANGER:
      showNotification({
        color: "red",
        title: title,
        message: detail
      });
      break;
  }
};

export function downloadFile(relativeURL?: string) {
  if (relativeURL === "" || relativeURL === undefined) return;

  let url = getApiURL();
  url.pathname += relativeURL;

  const token = getToken();
  if (token) {
    url.searchParams.set("token", token);
  }

  let link = document.createElement("a");
  link.download = "";
  link.target = "_blank";
  link.href = url.href;
  link.click();
  link.remove();
}
