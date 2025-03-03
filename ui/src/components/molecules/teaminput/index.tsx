import { useState, ChangeEvent } from 'react';
import { keyTeamCode, addUserToTeam, keyUUID, addConnection } from '../../../utils/supabase';

function TeamInput() {
  const [inputValue, setInputValue] = useState('');

  const handleInputChange = (event: ChangeEvent<HTMLInputElement>) => {
    setInputValue(event.target.value);
    localStorage.setItem(keyTeamCode, event.target.value);
  };

  const storedValue = localStorage.getItem(keyTeamCode);
  let placeHolder = "Enter team code here"
  if (storedValue) {
    placeHolder = storedValue
  }

  const handleClick = () => {
    const inviteCode = localStorage.getItem(keyTeamCode);
    if (inviteCode) {
      console.log("Adding user to team with invite code:", inviteCode);
      addUserToTeam(inviteCode)
    }
  };

  const log_uuid = () => {
    const local_uuid = localStorage.getItem(keyUUID);
    const local_teamCode = localStorage.getItem(keyTeamCode);
    console.log('local invite code:', local_teamCode, '| uuid:', local_uuid);
  };

  const simpulateConnection = () => {
   addConnection()
  };



  return (
    <div>
      <input
        type="text"
        value={inputValue}
        onChange={handleInputChange}
        placeholder={placeHolder}
      />
      <p>Current Team: {storedValue}</p>
      <button onClick={handleClick}>Join Team</button>
      <button onClick={log_uuid}>log_uuid</button>
      <button onClick={simpulateConnection}>simulate a connection</button>
    </div>
  );
}

export default TeamInput;