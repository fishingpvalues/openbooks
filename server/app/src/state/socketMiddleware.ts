import {
  AnyAction,
  Dispatch,
  Middleware,
  MiddlewareAPI,
  PayloadAction
} from "@reduxjs/toolkit";
import { openbooksApi } from "./api";
import { deleteHistoryItem } from "./historySlice";
import {
  ConnectionResponse,
  DownloadResponse,
  MessageType,
  Notification,
  NotificationType,
  Response,
  SearchResponse
} from "./messages";
import { addNotification } from "./notificationSlice";
import {
  removeInFlightDownload,
  sendMessage,
  setConnectionState,
  setSearchResults,
  setUsername
} from "./stateSlice";
import { AppDispatch, RootState } from "./store";
import { displayNotification, downloadFile } from "./util";

// Web socket redux middleware.
// Listens to socket and dispatches handlers.
// Handles send_message actions by sending to socket.
export const websocketConn =
  (wsUrl: string): Middleware =>
  ({ dispatch, getState }: MiddlewareAPI<AppDispatch, RootState>) => {
    const socket = new WebSocket(wsUrl);

    socket.onopen = () => onOpen(dispatch);
    socket.onclose = (event: CloseEvent) => onClose(dispatch, event.code);
    socket.onmessage = (message) => route(dispatch, message);
    socket.onerror = (event) =>
      displayNotification({
        appearance: NotificationType.DANGER,
        title: "Unable to connect to server.",
        timestamp: new Date().getTime()
      });

    // redux 5 types the middleware chain with unknown action/next params, so
    // narrow at the single entry point instead of per handler.
    return (next: (action: unknown) => unknown) => (action: unknown) => {
      const typedAction = action as AnyAction;
      // Send Message action? Send data to the socket.
      if (sendMessage.match(typedAction)) {
        if (socket.readyState === socket.OPEN) {
          socket.send(typedAction.payload.message);
        } else {
          displayNotification({
            appearance: NotificationType.WARNING,
            title: "Server connection closed. Reload page.",
            timestamp: new Date().getTime()
          });
        }
      }

      return next(typedAction);
    };
  };

const onOpen = (dispatch: AppDispatch): void => {
  console.log("WebSocket connected.");
  dispatch(setConnectionState(true));
  dispatch(sendMessage({ type: MessageType.CONNECT, payload: {} }));
};

const onClose = (dispatch: AppDispatch, closeCode: number): void => {
  console.log("WebSocket closed.");
  dispatch(setConnectionState(false));
  if (closeCode !== 1000) {
    displayNotification({
      appearance: NotificationType.WARNING,
      title: "Disconnected from server",
      detail: `Close code ${closeCode}. If this is unexpected, your token may be invalid - check it.`,
      timestamp: new Date().getTime()
    });
  }
};

const route = (dispatch: AppDispatch, msg: MessageEvent<any>): void => {
  const getNotif = (): Notification => {
    let response = JSON.parse(msg.data) as Response;
    const timestamp = new Date().getTime();
    const notification: Notification = {
      ...response,
      timestamp
    };

    switch (response.type) {
      case MessageType.STATUS:
        return notification;
      case MessageType.CONNECT:
        dispatch(setUsername((response as ConnectionResponse).name));
        return notification;
      case MessageType.SEARCH:
        dispatch(setSearchResults(response as SearchResponse));
        return notification;
      case MessageType.DOWNLOAD:
        downloadFile((response as DownloadResponse)?.downloadPath);
        dispatch(openbooksApi.util.invalidateTags(["books"]));
        dispatch(removeInFlightDownload());
        return notification;
      case MessageType.RATELIMIT:
        dispatch(deleteHistoryItem());
        return notification;
      default:
        console.error(response);
        return {
          appearance: NotificationType.DANGER,
          title: "Unknown message type. See console.",
          timestamp
        };
    }
  };

  const notif = getNotif();
  dispatch(addNotification(notif));
  displayNotification(notif);
};
