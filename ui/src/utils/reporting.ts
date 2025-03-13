export async function report() {
    const headers = {
        "X-Lantern-TeamID": 'fake-team-iden-tify',
        "X-Lantern-UserID": 'fake-user-iden-tify',
    };

    fetch('http://localhost:8888', {
        method: 'POST',
        headers: headers,
        body: JSON.stringify({})
    })
        .then(response => {
            console.log('report sent, response:', response);
        })
        .catch(error => {
            console.error('report send failed:', error);
        });
}
