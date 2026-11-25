import { Button } from "../../../../components/Button/Button";
import { Dialog, DialogText } from "../../../../components/Dialog/Dialog";
import type { Level } from "../../../../router/devices";
import { raiseLine, VERB } from "./model";

export interface RaiseDialogProps {
  device: string;
  level: Level;
  selfName: string;
  host: string;
  onConfirm: () => void;
  onCancel: () => void;
}

/** Raising a device to control or administer asks first. */
export function RaiseDialog({ device, level, selfName, host, onConfirm, onCancel }: RaiseDialogProps) {
  return (
    <Dialog
      alert
      confirm
      title={`Let ${device} ${VERB[level]} mads on this ${host}?`}
      onDismiss={onCancel}
      actions={
        <>
          <Button variant="ghost" onClick={onCancel}>
            Cancel
          </Button>
          <Button variant="primary" onClick={onConfirm}>
            Let it {VERB[level]}
          </Button>
        </>
      }
    >
      <DialogText>
        <p>{raiseLine(device, level, selfName)}</p>
        {level === "admin" && <p>Administer can only be given here, never from another device.</p>}
      </DialogText>
    </Dialog>
  );
}
