// Error of an API call (or of a local change): its message can be shown to
// the user, status is the HTTP status (0 when the server cannot be reached),
// data the decoded answer when there was one (an app's extra fields, such as
// the id of an existing duplicate).

export class ApiError extends Error {
  constructor(message, status, data = null) {
    super(message);
    this.status = status;
    this.data = data;
  }
}
